//go:build linux

package cni

import (
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func deleteLink(name string) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("cni: opening netlink socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("cni: binding netlink socket: %w", err)
	}
	timeout := unix.Timeval{Sec: 5}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &timeout); err != nil {
		return fmt.Errorf("cni: setting netlink timeout: %w", err)
	}

	nameBytes := append([]byte(name), 0)
	attrLen := 4 + len(nameBytes)
	attrAligned := (attrLen + 3) &^ 3
	total := unix.NLMSG_HDRLEN + 16 + attrAligned
	message := make([]byte, total)
	binary.LittleEndian.PutUint32(message[0:], uint32(total))
	binary.LittleEndian.PutUint16(message[4:], unix.RTM_DELLINK)
	binary.LittleEndian.PutUint16(message[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	binary.LittleEndian.PutUint32(message[8:], 1)

	linkInfo := unix.NLMSG_HDRLEN
	message[linkInfo] = unix.AF_UNSPEC
	binary.LittleEndian.PutUint32(message[linkInfo+12:], 0xffffffff)

	attribute := linkInfo + 16
	binary.LittleEndian.PutUint16(message[attribute:], uint16(attrLen))
	binary.LittleEndian.PutUint16(message[attribute+2:], unix.IFLA_IFNAME)
	copy(message[attribute+4:], nameBytes)

	if err := unix.Sendto(fd, message, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("cni: sending RTM_DELLINK for %q: %w", name, err)
	}
	return receiveNetlinkAck(fd)
}

func receiveNetlinkAck(fd int) error {
	response := make([]byte, 4096)
	for {
		read, _, err := unix.Recvfrom(fd, response, 0)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return fmt.Errorf("cni: reading netlink ack: %w", err)
		}
		for offset := 0; offset+unix.NLMSG_HDRLEN <= read; {
			length := int(binary.LittleEndian.Uint32(response[offset:]))
			messageType := binary.LittleEndian.Uint16(response[offset+4:])
			if length < unix.NLMSG_HDRLEN || offset+length > read {
				break
			}
			switch messageType {
			case unix.NLMSG_ERROR:
				if length < unix.NLMSG_HDRLEN+4 {
					return errors.New("cni: short netlink error message")
				}
				code := int32(binary.LittleEndian.Uint32(response[offset+unix.NLMSG_HDRLEN:]))
				if code == 0 {
					return nil
				}
				errno := syscall.Errno(-code)
				if errors.Is(errno, unix.ENODEV) {
					return errLinkNotFound
				}
				return fmt.Errorf("cni: RTM_DELLINK failed: %w", errno)
			case unix.NLMSG_DONE:
				return nil
			}
			offset += (length + 3) &^ 3
		}
	}
}
