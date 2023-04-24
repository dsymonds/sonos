package sonos

import (
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

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
}

func serviceClient(dev *goupnp.Device, serviceType string) (*soap.SOAPClient, error) {
	svcs := dev.FindService(serviceType)
	if len(svcs) == 0 {
		return nil, fmt.Errorf("unknown service %q for device", serviceType)
	}
	return svcs[0].NewSOAPClient(), nil
}

func (d *Device) soap(ctx context.Context, serviceType, action string, in, out interface{}) error {
	sc, err := serviceClient(d.dev, serviceType)
	if err != nil {
		return err
	}
	return sc.PerformActionCtx(ctx, serviceType, action, in, out)
}

func (c *Client) ZoneDevice(ctx context.Context, zone string) (*Device, error) {
	devs, ok := c.zones[zone]
	if !ok {
		return nil, fmt.Errorf("unknown zone %q, or it has no devices", zone)
	}

	// Find the first device in the zone with the AV1 service.
	// This should be the most usable device.
	for _, dev := range devs {
		_, err := serviceClient(dev, av1.URN_AVTransport_1)
		if err != nil {
			continue
		}
		return &Device{
			dev: dev,
		}, nil
	}
	return nil, fmt.Errorf("did not find an AV1 service in zone %q", zone)
}

func (d *Device) Ungroup(ctx context.Context) error {
	err := d.soap(ctx, av1.URN_AVTransport_1, "BecomeCoordinatorOfStandaloneGroup", struct {
		InstanceID string
	}{InstanceID: "0"}, &struct{}{})
	if err != nil {
		return fmt.Errorf("ungrouping: %w", err)
	}
	return nil
}

func (d *Device) ClearQueue(ctx context.Context) error {
	err := d.soap(ctx, av1.URN_AVTransport_1, "RemoveAllTracksFromQueue", struct {
		InstanceID string
	}{InstanceID: "0"}, &struct{}{})
	if err != nil {
		return fmt.Errorf("clearing queue: %w", err)
	}
	return nil
}

type PlayMode int

const (
	NormalPlayMode PlayMode = iota
	RepeatAll
	RepeatOne
	Shuffle
	ShuffleRepeat
	ShuffleRepeatOne
)

var playModeIDs = map[PlayMode]string{
	NormalPlayMode:   "NORMAL",
	RepeatAll:        "REPEAT_ALL",
	RepeatOne:        "REPEAT_ONE",
	Shuffle:          "SHUFFLE_NOREPEAT",
	ShuffleRepeat:    "SHUFFLE",
	ShuffleRepeatOne: "SHUFFLE_REPEAT_ONE",
}

func (d *Device) SetPlayMode(ctx context.Context, mode PlayMode) error {
	err := d.soap(ctx, av1.URN_AVTransport_1, "SetPlayMode", struct {
		InstanceID  string
		NewPlayMode string
	}{
		InstanceID:  "0",
		NewPlayMode: playModeIDs[mode],
	}, &struct{}{})
	if err != nil {
		return fmt.Errorf("setting play mode: %w", err)
	}
	return nil
}

// SetVolume sets the devices volume, in range [0,100].
func (d *Device) SetVolume(ctx context.Context, volume int) error {
	err := d.soap(ctx, "urn:schemas-upnp-org:service:RenderingControl:1", "SetVolume", struct {
		InstanceID    string
		Channel       string
		DesiredVolume string
	}{
		InstanceID:    "0",
		Channel:       "Master",
		DesiredVolume: strconv.Itoa(volume),
	}, &struct{}{})
	if err != nil {
		return fmt.Errorf("setting volume: %w", err)
	}
	return nil
}

func (d *Device) SetSleepTimer(ctx context.Context, duration time.Duration) error {
	var dur string
	if duration > 0 {
		hh := duration / time.Hour
		duration -= hh * time.Hour
		mm := duration / time.Minute
		duration -= mm * time.Minute
		ss := duration / time.Second
		dur = fmt.Sprintf("%02d:%02d:%02d", hh, mm, ss)
	}

	err := d.soap(ctx, av1.URN_AVTransport_1, "ConfigureSleepTimer", struct {
		InstanceID            string
		NewSleepTimerDuration string // "hh:mm:ss" or empty string
	}{
		InstanceID:            "0",
		NewSleepTimerDuration: dur,
	}, &struct{}{})
	if err != nil {
		return fmt.Errorf("setting sleep timer: %w", err)
	}
	return nil
}

func (d *Device) Play(ctx context.Context) error {
	err := d.soap(ctx, av1.URN_AVTransport_1, "Play", struct {
		InstanceID string
		Speed      string
	}{
		InstanceID: "0",
		Speed:      "1",
	}, &struct{}{})
	if err != nil {
		return fmt.Errorf("playing: %w", err)
	}
	return nil
}

func (d *Device) LoadSonosPlaylist(ctx context.Context, playlistName string) error {
	var raw struct {
		Result string // DIDL-Lite XML
	}
	err := d.soap(ctx, av1.URN_ContentDirectory_1, "Browse", struct {
		ObjectID       string
		BrowseFlag     string
		Filter         string
		StartingIndex  string
		RequestedCount string
		SortCriteria   string
	}{
		ObjectID:       "SQ:", // TODO: add a search query
		BrowseFlag:     "BrowseDirectChildren",
		Filter:         "*", // all fields
		StartingIndex:  "0",
		RequestedCount: "100",
		SortCriteria:   "+upnp:artist,+dc:title",
	}, &raw)
	if err != nil {
		return fmt.Errorf("browsing: %w", err)
	}

	var didl struct {
		Container []struct {
			ID    string `xml:"id,attr"`
			Title string `xml:"title"`
			Res   string `xml:"res"`
		} `xml:"container"`
	}
	if xml.Unmarshal([]byte(raw.Result), &didl); err != nil {
		return fmt.Errorf("unmarshaling DIDL-Lite XML: %w", err)
	}

	var uri string
	for _, c := range didl.Container {
		if c.Title == playlistName {
			uri = c.Res
			break
		}
	}
	if uri == "" {
		return fmt.Errorf("did not find Sonos playlist named %q (checked %d)", playlistName, len(didl.Container))
	}

	// Add the playlist.
	var resp struct {
		NumTracksAdded string
		NewQueueLength string
	}
	err = d.soap(ctx, av1.URN_AVTransport_1, "AddURIToQueue", struct {
		InstanceID                      string
		EnqueuedURI                     string
		EnqueuedURIMetaData             string
		DesiredFirstTrackNumberEnqueued string
		EnqueueAsNext                   string
	}{
		InstanceID:                      "0",
		EnqueuedURI:                     uri,
		DesiredFirstTrackNumberEnqueued: "1", // add to end
		EnqueueAsNext:                   "1",
	}, &resp)
	if err != nil {
		return fmt.Errorf("adding to queue: %w", err)
	}
	_ = resp // TODO: report stats

	return nil
}
