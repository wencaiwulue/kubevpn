package core

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
	"net"
	"os/exec"

	"github.com/datawire/dlib/derror"
)

type Device struct {
	tun.Device
	name           string
	dns            net.IP
	interfaceIndex uint32
}

func openTun(ctx context.Context) (td *Device, err error) {
	defer func() {
		if r := recover(); r != nil {
			var ok bool
			if err, ok = r.(error); !ok {
				err = derror.PanicToError(r)
			}
		}
	}()
	interfaceName := "wg1"
	td = &Device{}
	if td.Device, err = tun.CreateTUN(interfaceName, 0); err != nil {
		return nil, fmt.Errorf("failed to create TUN device: %w", err)
	}
	if td.name, err = td.Device.Name(); err != nil {
		return nil, fmt.Errorf("failed to get real name of TUN device: %w", err)
	}
	iface, err := td.getLUID().Interface()
	if err != nil {
		return nil, fmt.Errorf("failed to get interface for TUN device: %w", err)
	}
	td.interfaceIndex = iface.InterfaceIndex

	return td, nil
}

func (t *Device) Close() error {
	// The tun.NativeTun device has a closing mutex which is read locked during
	// a call to Read(). The read lock prevents a call to Close() to proceed
	// until Read() actually receives something. To resolve that "deadlock",
	// we call Close() in one goroutine to wait for the lock and write a bogus
	// message in another that will be returned by Read().
	closeCh := make(chan error)
	go func() {
		// first message is just to indicate that this goroutine has started
		closeCh <- nil
		closeCh <- t.Device.Close()
		close(closeCh)
	}()

	// Not 100%, but we can be fairly sure that Close() is
	// hanging on the lock, or at least will be by the time
	// the Read() returns
	<-closeCh

	// Send something to the TUN device so that the Read
	// unlocks the NativeTun.closing mutex and let the actual
	// Close call continue
	conn, err := net.Dial("udp", t.dns.String()+":53")
	if err == nil {
		_, _ = conn.Write([]byte("bogus"))
	}
	return <-closeCh
}

func (t *Device) getLUID() winipcfg.LUID {
	return winipcfg.LUID(t.Device.(*tun.NativeTun).LUID())
}

func (t *Device) addSubnet(_ context.Context, subnet *net.IPNet) error {
	return t.getLUID().AddIPAddress(*subnet)
}

func (t *Device) removeSubnet(_ context.Context, subnet *net.IPNet) error {
	return t.getLUID().DeleteIPAddress(*subnet)
}

func (t *Device) setDNS(ctx context.Context, server net.IP, domains []string) (err error) {
	ipFamily := func(ip net.IP) winipcfg.AddressFamily {
		f := winipcfg.AddressFamily(windows.AF_INET6)
		if ip4 := ip.To4(); ip4 != nil {
			f = windows.AF_INET
		}
		return f
	}
	family := ipFamily(server)
	luid := t.getLUID()
	if t.dns != nil {
		if oldFamily := ipFamily(t.dns); oldFamily != family {
			_ = luid.FlushDNS(oldFamily)
		}
	}
	if err = luid.SetDNS(family, []net.IP{server}, domains); err != nil {
		return err
	}
	_ = exec.CommandContext(ctx, "ipconfig", "/flushdns").Run()
	return nil
}

func (t *Device) setMTU(mtu int) error {
	return errors.New("not implemented")
}

func (t *Device) readPacket(into []byte) (int, error) {
	return t.Device.Read(into, 0)
}

func (t *Device) writePacket(from []byte) (int, error) {
	return t.Device.Write(from, 0)
}
