use std::collections::HashMap;

use serde::Deserialize;

#[derive(Deserialize)]
#[serde(transparent)]
pub struct BorrowedRawValue<'a>(#[serde(borrow)] pub &'a serde_json::value::RawValue);

#[derive(Deserialize, Default)]
pub struct InitConfig<'a> {
  #[serde(default)]
  #[serde(borrow)]
  pub gpio: Option<GpioConfig<'a>>,
}

#[derive(Deserialize)]
pub struct GpioConfig<'a> {
  #[serde(default)]
  #[serde(borrow)]
  pub pins: HashMap<String, BorrowedRawValue<'a>>, // GpioPinConfig
}

#[derive(Deserialize, Debug)]
pub struct GpioPinConfig {
  pub chip: u8,
  pub offset: u8,

  pub direction: GpioPinDirection,

  #[serde(default)]
  pub drive: Option<GpioPinDrive>,

  #[serde(default)]
  pub initial_level: Option<GpioPinLevel>,

  #[serde(default)]
  pub function: GpioPinFunction,
}

#[derive(Deserialize, Debug)]
#[serde(rename_all = "lowercase")]
pub enum GpioPinDirection {
  In,
  Out,
}

#[derive(Deserialize, Debug)]
#[serde(rename_all = "snake_case")]
pub enum GpioPinDrive {
  PushPull,
  OpenDrain,
}

#[derive(Deserialize, Debug)]
#[serde(rename_all = "snake_case")]
pub enum GpioPinLevel {
  High,
  Low,
}

#[derive(Deserialize, Debug)]
#[serde(rename_all = "snake_case")]
pub enum GpioPinFunction {
  ExtReset,
  LivenessBlink,
}

impl Default for GpioPinFunction {
  fn default() -> Self {
    Self::ExtReset
  }
}
