// Package rtsp contains the RTSP static source.
package rtsp

// tunnel:
import (
	gtunnel "gortsplib-tunnel"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/pion/rtp"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/tls"
)

// TunnelSource is a RTSP static source.
type TunnelSource struct {
	RtspUrl        string
	TunnelPort     uint
	ReadTimeout    conf.StringDuration
	WriteTimeout   conf.StringDuration
	WriteQueueSize int
	Parent         defs.StaticSourceParent
}

// Log implements StaticSource.
func (s *TunnelSource) Log(level logger.Level, format string, args ...interface{}) {
	s.Parent.Log(level, "[RTSP tunnel source] "+format, args...)
}

// Run implements StaticSource.
func (s *TunnelSource) Run(params defs.StaticSourceRunParams) error {
	s.Log(logger.Debug, "connecting")

	decodeErrLogger := logger.NewLimitedLogger(s)

	v := gtunnel.TransportTCP
	c := &gtunnel.ClientTunnel{
		Transport:      &v,
		TLSConfig:      tls.ConfigForFingerprint(params.Conf.SourceFingerprint),
		ReadTimeout:    time.Duration(s.ReadTimeout),
		WriteTimeout:   time.Duration(s.WriteTimeout),
		WriteQueueSize: s.WriteQueueSize,
		AnyPortEnable:  params.Conf.RTSPAnyPort,
		OnRequest: func(req *base.Request) {
			s.Log(logger.Debug, "[c->s] %v", req)
		},
		OnResponse: func(res *base.Response) {
			s.Log(logger.Debug, "[s->c] %v", res)
		},
		OnTransportSwitch: func(err error) {
			s.Log(logger.Warn, err.Error())
		},
		OnPacketLost: func(err error) {
			decodeErrLogger.Log(logger.Warn, err.Error())
		},
		OnDecodeError: func(err error) {
			decodeErrLogger.Log(logger.Warn, err.Error())
		},
	}

	// tunnel port
	err := c.SetTunnelPort(s.TunnelPort)
	if err != nil {
		return err
	}

	u, err := base.ParseURL(params.Conf.Source)
	if err != nil {
		return err
	}

	err = c.Start(u.Scheme, u.Host)
	if err != nil {
		return err
	}
	defer c.Close()

	readErr := make(chan error)
	go func() {
		readErr <- func() error {
			_, err := c.GET(u)
			if err != nil {
				return err
			}

			_, err = c.POST(u)
			if err != nil {
				return err
			}

			desc, _, err := c.Describe(u)
			if err != nil {
				return err
			}

			err = c.SetupAll(desc.BaseURL, desc.Medias)
			if err != nil {
				return err
			}

			res := s.Parent.SetReady(defs.PathSourceStaticSetReadyReq{
				Desc:               desc,
				GenerateRTPPackets: false,
			})
			if res.Err != nil {
				return res.Err
			}

			defer s.Parent.SetNotReady(defs.PathSourceStaticSetNotReadyReq{})

			for _, medi := range desc.Medias {
				for _, forma := range medi.Formats {
					cmedi := medi
					cforma := forma

					c.OnPacketRTP(cmedi, cforma, func(pkt *rtp.Packet) {
						pts, ok := c.PacketPTS(cmedi, pkt)
						if !ok {
							return
						}

						res.Stream.WriteRTPPacket(cmedi, cforma, pkt, time.Now(), pts)
					})
				}
			}

			rangeHeader, err := createRangeHeader(params.Conf)
			if err != nil {
				return err
			}

			_, err = c.Play(rangeHeader)
			if err != nil {
				return err
			}

			return c.Wait()
		}()
	}()

	for {
		select {
		case err := <-readErr:
			return err

		case <-params.ReloadConf:

		case <-params.Context.Done():
			c.Close()
			<-readErr
			return nil
		}
	}
}

// APISourceDescribe implements StaticSource.
func (*TunnelSource) APISourceDescribe() defs.APIPathSourceOrReader {
	return defs.APIPathSourceOrReader{
		Type: "rtspTunnelSource",
		ID:   "",
	}
}
