package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"runtime"
	"strconv"

	"github.com/c-robinson/iplib"
	"github.com/gravitl/netmaker/netclient/netclient-proxy/common"
	"github.com/gravitl/netmaker/netclient/netclient-proxy/packet"
	"github.com/gravitl/netmaker/netclient/netclient-proxy/wg"
)

func NewProxy(config Config) *Proxy {
	p := &Proxy{Config: config}
	p.Ctx, p.Cancel = context.WithCancel(context.Background())
	return p
}

// proxyToRemote proxies everything from Wireguard to the RemoteKey peer
func (p *Proxy) ProxyToRemote() {
	buf := make([]byte, 1500)
	for {
		select {
		case <-p.Ctx.Done():
			log.Printf("stopped proxying to remote peer %s due to closed connection\n", p.Config.RemoteKey)
			return
		default:

			n, err := p.LocalConn.Read(buf)
			if err != nil {
				log.Println("ERRR READ: ", err)
				continue
			}

			if peerI, ok := common.Peers[p.Config.RemoteKey]; ok {
				log.Println("PROCESSING PKT BEFORE SENDING")

				buf, n, err = packet.ProcessPacketBeforeSending(buf, n, peerI.Config.RemoteWgPort)
				if err != nil {
					log.Println("failed to process pkt before sending: ", err)
				}
			} else {
				log.Printf("Peer: %s not found in config\n", p.Config.RemoteKey)
			}
			// test(n, buf)
			log.Printf("PROXING TO REMOTE!!!---> %s >>>>> %s\n", p.Config.ProxyServer.Server.LocalAddr().String(), p.RemoteConn.RemoteAddr().String())
			host, port, _ := net.SplitHostPort(p.RemoteConn.RemoteAddr().String())
			portInt, _ := strconv.Atoi(port)
			_, err = p.Config.ProxyServer.Server.WriteToUDP(buf[:n], &net.UDPAddr{
				IP:   net.ParseIP(host),
				Port: portInt,
			})
			if err != nil {
				log.Println("Failed to send to remote: ", err)
			}
		}
	}
}

func test(n int, data []byte) {
	var localWgPort uint16
	//portBuf := data[n-2 : n+1]
	portBuf := data[2:4]
	reader := bytes.NewReader(portBuf)
	err := binary.Read(reader, binary.BigEndian, &localWgPort)
	if err != nil {
		log.Println("Failed to read port buffer: ", err)
	}
	log.Println("TEST WFGPO: ", localWgPort)
}

// proxyToLocal proxies everything from the RemoteKey peer to local Wireguard
func (p *Proxy) ProxyToLocal() {

	buf := make([]byte, 1500)
	for {
		select {
		case <-p.Ctx.Done():
			log.Printf("stopped proxying from remote peer %s due to closed connection\n", p.Config.RemoteKey)
			return
		default:

			n, err := p.RemoteConn.Read(buf)
			if err != nil {
				continue
			}
			log.Printf("PROXING TO LOCAL!!!---> %s <<<<<<<< %s\n", p.LocalConn.LocalAddr().String(), p.RemoteConn.RemoteAddr().String())
			_, err = p.LocalConn.Write(buf[:n])
			if err != nil {
				continue
			}
		}
	}
}

func (p *Proxy) updateEndpoint() error {
	udpAddr, err := net.ResolveUDPAddr("udp", p.LocalConn.LocalAddr().String())
	if err != nil {
		return err
	}
	log.Println("--------> UDPADDR:  ", udpAddr)
	// add local proxy connection as a Wireguard peer
	err = p.Config.WgInterface.UpdatePeer(p.Config.RemoteKey, p.Config.AllowedIps, wg.DefaultWgKeepAlive,
		udpAddr, p.Config.PreSharedKey)
	if err != nil {
		return err
	}

	return nil
}

func (p *Proxy) Start(remoteConn net.Conn) error {
	p.RemoteConn = remoteConn

	var err error
	addr, err := GetFreeIp("127.0.0.1/8")
	if err != nil {
		log.Println("Failed to get freeIp: ", err)
		return err
	}
	wgAddr := "127.0.0.1"
	if runtime.GOOS == "darwin" {
		wgAddr = addr
	}
	wgPort, err := p.Config.WgInterface.GetListenPort()
	if err != nil {
		log.Printf("Failed to get listen port for iface: %s,Err: %v\n", p.Config.WgInterface.Name, err)
		return err
	}

	p.LocalConn, err = net.DialUDP("udp", &net.UDPAddr{
		IP:   net.ParseIP(addr),
		Port: common.NmProxyPort,
	}, &net.UDPAddr{
		IP:   net.ParseIP(wgAddr),
		Port: *wgPort,
	})
	if err != nil {
		log.Fatalf("failed dialing to local Wireguard port,Err: %v\n", err)
	}

	log.Printf("Dialing to local Wireguard port %s --> %s\n", p.LocalConn.LocalAddr().String(), p.LocalConn.RemoteAddr().String())
	err = p.updateEndpoint()
	if err != nil {
		log.Printf("error while updating Wireguard peer endpoint [%s] %v\n", p.Config.RemoteKey, err)
		return err
	}

	go p.ProxyToRemote()

	return nil
}

func GetFreeIp(cidrAddr string) (string, error) {
	//ensure AddressRange is valid
	if _, _, err := net.ParseCIDR(cidrAddr); err != nil {
		log.Println("UniqueAddress encountered  an error")
		return "", err
	}
	net4 := iplib.Net4FromStr(cidrAddr)
	newAddrs := net4.FirstAddress()
	log.Println("COUNT: ", net4.Count())
	for {
		if runtime.GOOS == "darwin" {
			_, err := common.RunCmd(fmt.Sprintf("ifconfig lo0 alias %s 255.255.255.255", newAddrs.String()), true)
			if err != nil {
				log.Println("Failed to add alias: ", err)
			}
		}

		conn, err := net.DialUDP("udp", &net.UDPAddr{
			IP:   net.ParseIP(newAddrs.String()),
			Port: 51722,
		}, &net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: 51820,
		})
		if err == nil {
			conn.Close()
			return newAddrs.String(), nil
		}

		newAddrs, err = net4.NextIP(newAddrs)
		if err != nil {
			return "", err
		}

	}
}
