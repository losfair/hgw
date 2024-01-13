use std::{
  collections::HashMap,
  ffi::CStr,
  fs::File,
  io::Write,
  os::{
    fd::{AsRawFd, FromRawFd, RawFd},
    raw::c_char,
  },
  sync::{
    atomic::{AtomicBool, Ordering},
    Arc,
  },
  time::{Duration, Instant},
};

use heapless::BinaryHeap;
use linux_futex::{Futex, Private};
use tokio::{sync::Mutex, task::spawn_blocking};

use crate::{
  config::{
    GpioConfig, GpioPinConfig, GpioPinDirection, GpioPinDrive, GpioPinFunction, GpioPinLevel,
  },
  deadline::{enable_deadline, DeadlineSchedulerParams},
};

pub struct GpioService {
  requests: Arc<Mutex<std::os::unix::net::UnixDatagram>>,
}

impl GpioService {
  pub fn new(config: Option<&'static GpioConfig<'static>>) -> Self {
    let (tx, rx) = std::os::unix::net::UnixDatagram::pair().unwrap();
    std::thread::Builder::new()
      .name("gpio-server".to_string())
      .spawn(move || unsafe { gpio_server_thread(rx, config) })
      .expect("failed to spawn gpio thread");
    Self {
      requests: Arc::new(Mutex::new(tx)),
    }
  }

  pub async fn ext_reset(&self, pin_str: &str) -> Result<(), anyhow::Error> {
    let pin = pin_str.to_string();
    let requests = self.requests.clone().lock_owned().await;
    let success = spawn_blocking(move || {
      let pkt = ReqPacket {
        v: Req::Reset(ResetReq {
          pin: pin.as_str(),
          hold: Duration::from_secs(1),
        }),
        success: AtomicBool::new(false),
        completion: Futex::new(0),
      };
      let buf = (&pkt as *const ReqPacket as usize).to_ne_bytes();
      requests.send(&buf).unwrap();
      while pkt.completion.value.load(Ordering::SeqCst) == 0 {
        let _ = pkt.completion.wait(0);
      }
      pkt.success.load(Ordering::Relaxed)
    })
    .await?;

    if success {
      Ok(())
    } else {
      Err(anyhow::anyhow!("failed to reset pin {}", pin_str))
    }
  }
}

struct ReqPacket {
  v: Req,
  success: AtomicBool,
  completion: Futex<Private>,
}

enum Req {
  Reset(ResetReq),
}

struct ResetReq {
  pin: *const str,
  hold: Duration,
}

unsafe fn gpio_server_thread(
  rx: std::os::unix::net::UnixDatagram,
  config: Option<&'static GpioConfig<'static>>,
) {
  let mut kmsg = File::options()
    .read(true)
    .write(true)
    .open("/dev/kmsg")
    .expect("failed to open /dev/kmsg");
  let operator = Operator::new(config);

  let mut reset_deadlines: BinaryHeap<(Instant, RawFd), heapless::binary_heap::Min, 8> =
    BinaryHeap::new();

  rx.set_nonblocking(true)
    .expect("gpio_server_thread: failed to set nonblocking");

  tracing::info!("gpio thread started");
  enable_deadline(DeadlineSchedulerParams {
    runtime: Duration::from_millis(5),
    period: Duration::from_millis(100),
  })
  .expect("gpio_server_thread: failed to enable deadline scheduler");

  let mut buf_line_values: crate::gpiod_sys::gpio_v2_line_values = std::mem::zeroed();
  let mut loopcnt = 0u64;

  loop {
    libc::sched_yield();

    let now = Instant::now();
    let deadline = now + Duration::from_millis(4);

    loopcnt += 1;
    for f in &operator.blink_lines {
      buf_line_values.bits = loopcnt % 2;
      buf_line_values.mask = 1;

      // ignore error
      libc::ioctl(
        f.as_raw_fd(),
        crate::gpiod_sys::GPIO_V2_LINE_SET_VALUES_IOCTL_C as _,
        &mut buf_line_values,
      );
    }

    // External reset deadlines
    while let Some((reset_deadline, fd)) = reset_deadlines.peek() {
      if *reset_deadline > now {
        break;
      }

      buf_line_values.bits = 1;
      buf_line_values.mask = 1;

      let ret = libc::ioctl(
        *fd,
        crate::gpiod_sys::GPIO_V2_LINE_SET_VALUES_IOCTL_C as _,
        &mut buf_line_values,
      );
      if ret < 0 {
        kmsg
          .write_all(b"gpio_server_thread: reset_deadline: GPIO_V2_LINE_SET_VALUES_IOCTL failed\n")
          .unwrap();
      }

      reset_deadlines.pop().unwrap();
    }

    // Process incoming requests
    while Instant::now() < deadline {
      let mut recvbuf = [0u8; std::mem::size_of::<usize>()];
      let pkt = match rx.recv(&mut recvbuf) {
        Ok(x) if x == recvbuf.len() => GuardedReqPacket {
          raw: &*(usize::from_ne_bytes(recvbuf) as *const ReqPacket),
        },
        Ok(x) => panic!("gpio_server_thread: recv returned {}", x),
        Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => break,
        Err(e) => panic!("gpio_server_thread: recv returned {}", e),
      };

      match &pkt.raw.v {
        Req::Reset(x) => {
          let pin = &*x.pin;
          let file = match operator.ext_reset_lines.get(pin) {
            Some(x) => x,
            None => {
              continue;
            }
          };

          let fd = file.as_raw_fd();

          if reset_deadlines.iter().any(|x| x.1 == fd) {
            // Already in reset
            kmsg
              .write_all(b"gpio_server_thread: Req::Reset: already in reset\n")
              .unwrap();
            continue;
          }

          buf_line_values.bits = 0;
          buf_line_values.mask = 1;
          let ret = libc::ioctl(
            fd,
            crate::gpiod_sys::GPIO_V2_LINE_SET_VALUES_IOCTL_C as _,
            &mut buf_line_values,
          );
          if ret < 0 {
            kmsg
              .write_all(b"gpio_server_thread: Req::Reset: GPIO_V2_LINE_SET_VALUES_IOCTL failed\n")
              .unwrap();
            continue;
          }

          let success = reset_deadlines.push((Instant::now() + x.hold, fd)).is_ok();
          pkt.raw.success.store(success, Ordering::Relaxed);
        }
      }
    }
  }
}

struct GuardedReqPacket<'a> {
  raw: &'a ReqPacket,
}

impl<'a> Drop for GuardedReqPacket<'a> {
  fn drop(&mut self) {
    self.raw.completion.value.store(1, Ordering::SeqCst);
    self.raw.completion.wake(1);
  }
}

struct Operator {
  _chips: Vec<File>,
  ext_reset_lines: HashMap<&'static str, File>,
  blink_lines: Vec<File>,
}

impl Operator {
  unsafe fn new(config: Option<&'static GpioConfig<'static>>) -> Self {
    let mut chips: Vec<File> = Vec::new();
    for i in 0.. {
      let path = format!("/dev/gpiochip{}", i);
      match File::options().read(true).write(true).open(path) {
        Ok(x) => chips.push(x),
        Err(_) => break,
      }
    }

    for chip in &chips {
      let chip_info: crate::gpiod_sys::gpiochip_info = std::mem::zeroed();
      let ret = libc::ioctl(
        chip.as_raw_fd(),
        crate::gpiod_sys::GPIO_GET_CHIPINFO_IOCTL_C as _,
        &chip_info,
      );
      if ret != 0 {
        panic!(
          "gpio_server_thread: ioctl: {}",
          std::io::Error::last_os_error()
        );
      }

      let name = CStr::from_ptr(chip_info.name.as_ptr())
        .to_str()
        .unwrap_or_default();
      let label = CStr::from_ptr(chip_info.label.as_ptr())
        .to_str()
        .unwrap_or_default();
      tracing::info!(name, label, lines = chip_info.lines, "new gpio chip");
    }

    let mut ext_reset_lines: HashMap<&'static str, File> = HashMap::new();
    let mut blink_lines: Vec<File> = Vec::new();

    if let Some(config) = config {
      for (pin_name, pin_config) in &config.pins {
        let pin_config: GpioPinConfig = match serde_json::from_str(pin_config.0.get()) {
          Ok(x) => x,
          Err(error) => {
            tracing::error!(pin_name, ?error, "invalid gpio pin configuration");
            continue;
          }
        };

        let chip = match chips.get(pin_config.chip as usize) {
          Some(x) => x,
          None => {
            tracing::error!(pin_name, "invalid gpio chip");
            continue;
          }
        };
        /*
        pub offsets: [__u32; 64usize],
        pub consumer: [::std::os::raw::c_char; 32usize],
        pub config: gpio_v2_line_config,
        pub num_lines: __u32,
        pub event_buffer_size: __u32,
        pub padding: [__u32; 5usize],
        pub fd: __s32,
         */
        let mut flags: crate::gpiod_sys::gpio_v2_line_flag = 0;
        match pin_config.direction {
          GpioPinDirection::In => {
            flags |= crate::gpiod_sys::gpio_v2_line_flag_GPIO_V2_LINE_FLAG_INPUT;
          }
          GpioPinDirection::Out => {
            flags |= crate::gpiod_sys::gpio_v2_line_flag_GPIO_V2_LINE_FLAG_OUTPUT;
          }
        }
        if let Some(drive) = &pin_config.drive {
          match drive {
            GpioPinDrive::OpenDrain => {
              flags |= crate::gpiod_sys::gpio_v2_line_flag_GPIO_V2_LINE_FLAG_OPEN_DRAIN;
            }
            GpioPinDrive::PushPull => {}
          }
        }
        let default_value = if let Some(x) = &pin_config.initial_level {
          if matches!(x, GpioPinLevel::High) {
            1u32
          } else {
            0u32
          }
        } else {
          0u32
        };
        let mut req: crate::gpiod_sys::gpio_v2_line_request = std::mem::zeroed();
        req.offsets[0] = pin_config.offset as _;
        req.config.flags = flags as _;
        req.config.attrs[0].mask = 1;
        req.config.attrs[0].attr.id =
          crate::gpiod_sys::gpio_v2_line_attr_id_GPIO_V2_LINE_ATTR_ID_OUTPUT_VALUES;
        req.config.attrs[0].attr.__bindgen_anon_1.values = default_value as _;
        req.config.num_attrs = 1;
        let consumer = b"gpio-server\0";
        req.consumer[..consumer.len()]
          .copy_from_slice(std::mem::transmute::<&[u8], &[c_char]>(&consumer[..]));
        req.num_lines = 1;

        let ret = libc::ioctl(
          chip.as_raw_fd(),
          crate::gpiod_sys::GPIO_V2_GET_LINE_IOCTL_C as _,
          &mut req,
        );
        if ret < 0 {
          tracing::error!(
            pin_name,
            error = %std::io::Error::last_os_error(),
            "GPIO_V2_GET_LINE_IOCTL failed",
          );
          continue;
        }
        assert!(req.fd > 0);
        let line = File::from_raw_fd(req.fd);

        match pin_config.function {
          GpioPinFunction::ExtReset => {
            ext_reset_lines.insert(pin_name.as_str(), line);
          }
          GpioPinFunction::LivenessBlink => {
            blink_lines.push(line);
          }
        }

        tracing::info!(pin_name, ?pin_config, "opened gpio line");
      }
    }
    Self {
      _chips: chips,
      ext_reset_lines,
      blink_lines,
    }
  }
}
