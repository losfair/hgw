mod config;
mod deadline;
mod gpiod;
mod gpiod_sys;
mod rt_task;

use std::{fs::File, io::Read, os::fd::FromRawFd, sync::Mutex};

use config::InitConfig;
use tokio::net::UnixListener;
use tokio_stream::wrappers::UnixListenerStream;
use warp::Filter;

use crate::rt_task::gpio_server::GpioService;

fn main() {
  unsafe {
    if libc::mlockall(libc::MCL_CURRENT | libc::MCL_FUTURE) != 0 {
      panic!("mlockall failed: {:?}", std::io::Error::last_os_error());
    }
  }

  let rt = tokio::runtime::Builder::new_current_thread()
    .enable_all()
    .build()
    .unwrap();
  rt.block_on(async_main());
}

async fn async_main() {
  println!("locked all pages, initializing homegw-rt");

  let log_pipe = unsafe { File::from_raw_fd(3) };
  let server_listener = unsafe { std::os::unix::net::UnixListener::from_raw_fd(4) };
  if log_pipe.metadata().is_ok() && server_listener.set_nonblocking(true).is_ok() {
    tracing_subscriber::fmt()
      .with_max_level(tracing::Level::INFO)
      .with_writer(Mutex::new(log_pipe))
      .json()
      .init();
  } else {
    panic!("pre-opened file descriptors not found")
  }

  let mut config: Vec<u8> = Vec::new();
  std::io::stdin()
    .read_to_end(&mut config)
    .expect("failed to read config");
  let config: &'static [u8] = Vec::leak(config);
  let config: &'static InitConfig<'static> = Box::leak(Box::new(
    serde_json::from_slice::<InitConfig>(config).unwrap_or_else(|e| {
      tracing::error!("failed to parse config, using default: {}", e);
      InitConfig::default()
    }),
  ));

  std::thread::spawn(|| unsafe { rt_task::oom::oom_thread() });
  let gpio_service: &'static GpioService =
    Box::leak(Box::new(GpioService::new(config.gpio.as_ref())));

  let server_listener = UnixListener::from_std(server_listener).unwrap();
  let hello = warp::path!("hello" / String).map(|name| format!("Hello, {}!", name));
  let ext_reset = warp::path!("ext_reset" / String).then(move |pin_name: String| async move {
    match gpio_service.ext_reset(&pin_name).await {
      Ok(_) => warp::reply::with_status("OK".to_string(), warp::http::StatusCode::OK),
      Err(e) => {
        warp::reply::with_status(e.to_string(), warp::http::StatusCode::INTERNAL_SERVER_ERROR)
      }
    }
  });

  tracing::info!("homegw-rt is ready");

  warp::serve(hello.or(ext_reset))
    .serve_incoming(UnixListenerStream::new(server_listener))
    .await;
}
