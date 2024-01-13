package tesla_api

import (
	"context"
	"fmt"
	"time"

	"github.com/bogosj/tesla"
	"github.com/cenkalti/backoff/v4"
	"github.com/losfair/hgw/homegw-libs/concurrency"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

const MAX_CONCURRENCY = 3

type TeslaApiConfig struct {
	// API client config
	OauthToken string `json:"oauth_token"`
	Vin        string `json:"vin"`
}

type VehicleApi struct {
	client  *tesla.Client
	vehicle *concurrency.ValueTask[tesla.Vehicle]
	Vin     string
	Sem     chan struct{}
}

func NewVehicleApi(ctx context.Context, logger *zap.Logger, c *TeslaApiConfig) (*VehicleApi, error) {
	tesla.OAuth2Config.Endpoint.AuthURL = "https://auth.tesla.cn/oauth2/v3/authorize"
	tesla.OAuth2Config.Endpoint.TokenURL = "https://auth.tesla.cn/oauth2/v3/token"
	client, err := tesla.NewClient(ctx, tesla.WithToken(&oauth2.Token{
		AccessToken:  "",
		TokenType:    "Bearer",
		RefreshToken: c.OauthToken,
		Expiry:       time.Unix(1, 0),
	}), tesla.WithBaseURL("https://owner-api.vn.cloud.tesla.cn/api/1"))
	if err != nil {
		return nil, err
	}

	vehicle := concurrency.NewValueTask(func() *tesla.Vehicle {
		return fetchVehicle(ctx, logger, client, c.Vin)
	})

	return &VehicleApi{
		client:  client,
		vehicle: vehicle,
		Vin:     c.Vin,
		Sem:     make(chan struct{}, MAX_CONCURRENCY),
	}, nil
}

func (v *VehicleApi) Vehicle() *concurrency.ValueTask[tesla.Vehicle] {
	return v.vehicle
}

func fetchVehicle(ctx context.Context, logger *zap.Logger, client *tesla.Client, vin string) *tesla.Vehicle {
	var ret *tesla.Vehicle

	err := backoff.RetryNotify(func() error {
		logger.Info("attempting to load vehicle list")
		vehicles, err := client.Vehicles()
		if err != nil {
			return err
		}

		selected, ok := lo.Find(vehicles, func(v *tesla.Vehicle) bool {
			return v.Vin == vin
		})
		if !ok {
			return fmt.Errorf("vehicle with VIN %s not found", vin)
		}

		ret = selected
		return nil
	}, infiniteExponentialBackoff(ctx), func(err error, _ time.Duration) {
		logger.Error("failed to fetch vehicles", zap.Error(err))
	})

	if err != nil {
		logger.Error("canceled vehicle list fetch", zap.Error(err))
	} else {
		logger.Info("selected vehicle", zap.Any("vehicle", ret))
	}

	return ret
}
