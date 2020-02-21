package pcap

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
	"github.com/jackpal/gateway"
)

// IPAddr describes address with ip and mask
type IPAddr struct {
	IP   net.IP
	Mask net.IPMask
}

func (addr *IPAddr) IsIPv4() bool {
	return addr.IP.To4() != nil
}

func (addr IPAddr) String() string {
	ipnet := net.IPNet{IP:addr.IP, Mask:addr.Mask}
	return ipnet.String()
}

// Device describes an network device
type Device struct {
	Name         string
	FriendlyName string
	IPAddrs      []IPAddr
	HardwareAddr net.HardwareAddr
	IsLoop       bool
}

func (dev *Device) IPv4() *IPAddr {
	for _, addr := range dev.IPAddrs {
		if addr.IP.To4() != nil {
			return &addr
		}
	}
	return nil
}

func (dev *Device) IPv6() *IPAddr {
	for _, addr := range dev.IPAddrs {
		if addr.IP.To4() == nil && addr.IP.To16() != nil {
			return &addr
		}
	}
	return nil
}

func (dev Device) String() string {
	var result string
	if dev.HardwareAddr != nil {
		result = dev.Name + " [" + dev.HardwareAddr.String() + "]: "
	} else {
		result = dev.Name + ": "
	}
	for i, addr := range dev.IPAddrs {
		result = result + addr.IP.String()
		if i < len(dev.IPAddrs)-1 {
			result = result + ", "
		}
	}
	if dev.IsLoop {
		result = result + " (Loopback)"
	}
	return result
}

const flagPcapLoopback = 1

// FindAllDevs returns all valid network devices in current computer
func FindAllDevs() ([]*Device, error) {
	t := make([]*Device, 0)
	result := make([]*Device, 0)

	// Enumerate system's network interfaces
	inters, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("find all devs: %w", err)
	}
	for _, inter := range inters {
		// Ignore not up and not loopback interfaces
		if inter.Flags&net.FlagUp == 0 && inter.Flags&net.FlagLoopback == 0 {
			continue
		}
		var isLoop bool
		if inter.Flags&net.FlagLoopback != 0 {
			isLoop = true
		}
		addrs, err := inter.Addrs()
		if err != nil {
			fmt.Println(fmt.Errorf("find all devs: %w", err))
			continue
		}
		as := make([]IPAddr, 0)
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				fmt.Println(fmt.Errorf("find all devs: %w",
					fmt.Errorf("invalid address in %s", inter.Name)))
				continue
			}
			as = append(as, IPAddr{IP:ipnet.IP, Mask:ipnet.Mask})
		}
		t = append(t, &Device{FriendlyName:inter.Name, IPAddrs:as, HardwareAddr:inter.HardwareAddr, IsLoop:isLoop})
	}

	// Enumerate pcap devices
	devs, err := pcap.FindAllDevs()
	if err != nil {
		return nil, fmt.Errorf("find all devs: %w", err)
	}
	for _, dev := range devs {
		// Match pcap device with interface
		if dev.Flags&flagPcapLoopback != 0 {
			d := findLoopDev(t)
			if d == nil {
				continue
			}
			if d.Name != "" {
				return nil, fmt.Errorf("find all devs: %w", errors.New("too many loopback devices"))
			}
			d.Name = dev.Name
			result = append(result, d)
		} else {
			if len(dev.Addresses) <= 0 {
				continue
			}
			for _, addr := range dev.Addresses {
				d := findDev(t, addr.IP)
				if d == nil {
					continue
				}
				if d.Name != "" {
					return nil,fmt.Errorf("find all devs: %w", errors.New("multiple devices with same address"))
				}
				d.Name = dev.Name
				result = append(result, d)
				break
			}
		}
	}

	return result, nil
}

func findLoopDev(devs []*Device) *Device {
	for _, dev := range devs {
		if dev.IsLoop {
			return dev
		}
	}
	return nil
}

func findDev(devs []*Device, ip net.IP) *Device {
	for _, dev := range devs {
		for _, addr := range dev.IPAddrs {
			if addr.IP.Equal(ip) {
				return dev
			}
		}
	}
	return nil
}

// FindLoopDev returns the loop device in current computer
func FindLoopDev() (*Device, error) {
	devs, err := FindAllDevs()
	if err != nil {
		return nil, fmt.Errorf("find loop dev: %w", err)
	}
	return findLoopDev(devs), nil
}

// FindGatewayAddr returns the gatewayDev address
func FindGatewayAddr() (*IPAddr, error) {
	ip, err := gateway.DiscoverGateway()
	if err != nil {
		return nil, fmt.Errorf("find gateway addr: %w", err)
	}
	return &IPAddr{IP:ip}, nil
}

// FindGatewayDev returns the gatewayDev device
func FindGatewayDev(dev string) (*Device, error) {
	// Find gateway's IP
	ip, err := gateway.DiscoverGateway()
	if err != nil {
		return nil, fmt.Errorf("find gateway dev: %w", err)
	}

	// Create a packet capture for testing
	handle, err := pcap.OpenLive(dev, 1600, true, pcap.BlockForever)
	if err != nil {
		return nil, fmt.Errorf("find gateway dev: %w", err)
	}
	err = handle.SetBPFFilter(fmt.Sprintf("udp and dst %s and dst port 65535", ip.String()))
	if err != nil {
		return nil, fmt.Errorf("find gateway dev: %w", err)
	}
	localPacketSrc := gopacket.NewPacketSource(handle, handle.LinkType())
	c := make(chan gopacket.Packet, 1)
	go func() {
		for packet := range localPacketSrc.Packets() {
			c <- packet
			break
		}
	}()
	go func() {
		time.Sleep(3 * time.Second)
		c <- nil
	}()

	// Attempt to send and capture a UDP packet
	err = SendUDPPacket(ip.String()+":65535", []byte("0"))
	if err != nil {
		return nil, fmt.Errorf("find gateway dev: %w", err)
	}

	// Analyze the packet and get gateway's hardware address
	packet := <-c
	if packet == nil {
		return nil, fmt.Errorf("find gateway dev: %w", errors.New("timeout"))
	}
	ethernetLayer := packet.Layer(layers.LayerTypeEthernet)
	if ethernetLayer == nil {
		return nil, fmt.Errorf("find gateway dev: %w", errors.New("layer type out of range"))
	}
	ethernetPacket, ok := ethernetLayer.(*layers.Ethernet)
	if !ok {
		return nil, fmt.Errorf("find gateway dev: %w", errors.New("invalid packet"))
	}
	addrs := append(make([]IPAddr, 0), IPAddr{IP:ip})
	return &Device{FriendlyName:"Gateway", IPAddrs:addrs, HardwareAddr:ethernetPacket.DstMAC}, nil
}
