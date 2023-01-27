package sonos

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/huin/goupnp"
	"github.com/huin/goupnp/dcps/av1"
	"github.com/huin/goupnp/soap"
)

const (
	devPropertiesService = "urn:schemas-upnp-org:service:DeviceProperties:1"
)

type Client struct {
	devices []*goupnp.Device
	zones   map[string][]*goupnp.Device // devices, grouped by zone
}

func Discover(ctx context.Context) (*Client, error) {
	c := &Client{
		zones: make(map[string][]*goupnp.Device),
	}

	mrds, err := goupnp.DiscoverDevices(devPropertiesService)
	if err != nil {
		return nil, fmt.Errorf("discovering AV1: %w", err)
	}
	for _, mrd := range mrds {
		if mrd.Err != nil {
			log.Printf("Probing AV1 at %s: %v", mrd.Location, mrd.Err)
			continue
		}
		dev := &mrd.Root.Device
		// Only try to work with Sonos (or SYMFONISK) devices.
		if !strings.Contains(dev.Manufacturer, "Sonos, Inc.") {
			continue
		}
		c.devices = append(c.devices, dev)

		svcs := dev.FindService(devPropertiesService)
		if len(svcs) == 0 {
			continue
		}
		sc := svcs[0].NewSOAPClient()
		var resp struct {
			CurrentZoneName string
		}
		err := sc.PerformActionCtx(ctx, svcs[0].ServiceType, "GetZoneAttributes", struct{}{}, &resp)
		if err != nil {
			log.Printf("getting zone attributes: %v", err)
			continue
		}
		zone := resp.CurrentZoneName
		c.zones[zone] = append(c.zones[zone], dev)
	}

	return c, nil
}

type Device struct {
	dev *goupnp.Device
	av1 *soap.SOAPClient
}

func (c *Client) ZoneDevice(ctx context.Context, zone string) (*Device, error) {
	// Find the first device in the zone with the AV1 service.
	// This should be the most usable device.
	for _, dev := range c.zones[zone] {
		svcs := dev.FindService(av1.URN_AVTransport_1)
		if len(svcs) == 0 {
			continue
		}
		return &Device{
			dev: dev,
			av1: svcs[0].NewSOAPClient(),
		}, nil
	}
	return nil, fmt.Errorf("did not find an AV1 service in zone %q", zone)
}

func (d *Device) Ungroup(ctx context.Context) error {
	err := d.av1.PerformActionCtx(ctx, av1.URN_AVTransport_1, "BecomeCoordinatorOfStandaloneGroup", struct {
		InstanceID string
	}{InstanceID: "0"}, &struct{}{})
	if err != nil {
		return fmt.Errorf("ungrouping: %w", err)
	}
	return nil
}
