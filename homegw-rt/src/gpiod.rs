#[derive(Clone, Copy, Debug, Eq, PartialEq, Hash, Ord, PartialOrd)]
pub struct GpioPin {
  pub chip: u8,
  pub offset: u8,
}
