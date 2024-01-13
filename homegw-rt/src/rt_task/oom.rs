use crate::deadline::enable_fifo;

pub unsafe fn oom_thread() {
  let trig = b"full 150000 1000000\0";
  let mut fds = libc::pollfd {
    fd: 0,
    events: libc::POLLPRI,
    revents: 0,
  };

  fds.fd = libc::open(
    "/proc/pressure/memory\0".as_ptr() as *const libc::c_char,
    libc::O_RDWR | libc::O_NONBLOCK,
  );

  if fds.fd < 0 {
    panic!(
      "open /proc/pressure/memory: {}",
      std::io::Error::last_os_error()
    );
  }

  if libc::write(fds.fd, trig.as_ptr() as *const libc::c_void, trig.len()) < 0 {
    panic!(
      "write /proc/pressure/memory: {}",
      std::io::Error::last_os_error()
    );
  }

  let proc = libc::opendir("/proc\0".as_ptr() as *const libc::c_char);
  if proc.is_null() {
    panic!("opendir /proc: {}", std::io::Error::last_os_error());
  }

  libc::chdir("/proc\0".as_ptr() as *const libc::c_char);

  let dev_kmsg = libc::open(
    "/dev/kmsg\0".as_ptr() as *const libc::c_char,
    libc::O_WRONLY | libc::O_CLOEXEC,
  );
  if dev_kmsg < 0 {
    panic!("open /dev/kmsg: {}", std::io::Error::last_os_error());
  }

  tracing::info!("oom thread started, waiting for memory pressure event");
  enable_fifo(1).expect("failed to enable fifo scheduler");

  loop {
    let n = libc::poll(&mut fds, 1, -1);
    if n < 0 {
      panic!("poll: {}", std::io::Error::last_os_error());
    }
    if fds.revents & libc::POLLERR != 0 {
      panic!("got POLLERR, event source is gone");
    }
    if fds.revents & libc::POLLPRI != 0 {
      kill_non_root(proc);
      let buf = b"homegw-rt: killed all non-root processes\n";
      libc::write(
        dev_kmsg,
        buf.as_ptr() as *const libc::c_void,
        buf.len() as libc::size_t,
      );
    } else {
      panic!("unexpected event: 0x{:x}", fds.revents);
    }
  }
}

/// Walk /proc and kill all non-root processes
unsafe fn kill_non_root(proc: *mut libc::DIR) {
  let mut stat = std::mem::zeroed::<libc::stat>();
  libc::rewinddir(proc);
  let mut entry = libc::readdir(proc);
  while !entry.is_null() {
    if let Some(pid) = std::ffi::CStr::from_ptr((*entry).d_name.as_ptr())
      .to_str()
      .ok()
      .and_then(|x| x.parse::<libc::pid_t>().ok())
    {
      if libc::stat((*entry).d_name.as_ptr(), &mut stat) == 0 && stat.st_uid >= 1000 {
        libc::kill(pid, libc::SIGKILL);
      }
    };

    entry = libc::readdir(proc);
  }
}
