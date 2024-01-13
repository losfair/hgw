use std::time::Duration;

#[allow(dead_code)]
mod consts {
  pub const SCHED_NORMAL: u32 = 0;
  pub const SCHED_FIFO: u32 = 1;
  pub const SCHED_RR: u32 = 2;
  pub const SCHED_BATCH: u32 = 3;
  pub const SCHED_IDLE: u32 = 5;
  pub const SCHED_DEADLINE: u32 = 6;
}

#[derive(Copy, Clone, Debug)]
pub struct DeadlineSchedulerParams {
  pub runtime: Duration,
  pub period: Duration,
}

#[derive(Copy, Clone)]
#[repr(C)]
#[allow(non_camel_case_types)]
struct sched_attr {
  pub size: u32,
  pub sched_policy: u32,
  pub sched_flags: u64,
  pub sched_nice: i32,
  pub sched_priority: u32,
  pub sched_runtime: u64,
  pub sched_deadline: u64,
  pub sched_period: u64,
}

pub unsafe fn enable_deadline(params: DeadlineSchedulerParams) -> Result<(), std::io::Error> {
  let mut attr = sched_attr {
    size: std::mem::size_of::<sched_attr>() as u32,
    sched_policy: consts::SCHED_DEADLINE,
    sched_flags: 0,
    sched_nice: 0,
    sched_priority: 0,
    sched_runtime: params.runtime.as_nanos() as u64,
    sched_deadline: params.period.as_nanos() as u64,
    sched_period: params.period.as_nanos() as u64,
  };

  if libc::syscall(libc::SYS_sched_setattr, 0, &mut attr as *mut sched_attr, 0) < 0 {
    Err(std::io::Error::last_os_error())
  } else {
    Ok(())
  }
}

pub unsafe fn enable_fifo(priority: i32) -> Result<(), std::io::Error> {
  let mut attr = sched_attr {
    size: std::mem::size_of::<sched_attr>() as u32,
    sched_policy: consts::SCHED_FIFO,
    sched_flags: 0,
    sched_nice: 0,
    sched_priority: priority as u32,
    sched_runtime: 0,
    sched_deadline: 0,
    sched_period: 0,
  };

  if libc::syscall(libc::SYS_sched_setattr, 0, &mut attr as *mut sched_attr, 0) < 0 {
    Err(std::io::Error::last_os_error())
  } else {
    Ok(())
  }
}
