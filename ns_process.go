package main

import (
	"fmt"
	"github.com/docker/docker/pkg/reexec"
	"github.com/vishvananda/netlink"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type Object struct {
}

func init() {
	reexec.Register("nsInitialisation", nsInitialisation)
	if reexec.Init() {
		os.Exit(0)
	}
}

func nsInitialisation() {
	fmt.Printf("\n>> namespace setup ... <<\n\n")

	rootPath := os.Args[1]
	if err := pivotRoot(rootPath); err != nil {
		fmt.Printf("Error mount rootfs, %s\n", err)
		os.Exit(1)
	}

	if err := waitForNetwork(); err != nil {
		fmt.Printf("%s\n", err)
		os.Exit(1)
	}

	if err := setupNetwork(); err != nil {
		fmt.Printf("%s\n", err)
		os.Exit(1)
	}

	nsRun()
}

func nsRun() {
	cmd := exec.Command("/bin/sh")

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	cmd.Env = []string{"PS1= - ns process - #"}
	if err := cmd.Run(); err != nil {
		fmt.Printf("Error running command - %s\n", err)
		os.Exit(1)
	}
}

func pivotRoot(newroot string) (err error) {
	pivotroot := ".pivot_root"
	putold := filepath.Join(newroot, pivotroot)

	proc := filepath.Join(newroot, "proc")
	if err := os.MkdirAll(proc, 0755); err != nil {
		return err
	}
	if err := syscall.Mount(proc, proc, "proc", 0, ""); err != nil {
		return err
	}
	if err := syscall.Mount(newroot, newroot, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return err
	}

	if err := os.MkdirAll(putold, 0700); err != nil {
		return err
	}
	if err := syscall.PivotRoot(newroot, putold); err != nil {
		return err
	}
	if err := os.Chdir("/"); err != nil {
		return err
	}

	putold = filepath.Join("/", pivotroot)
	if err := syscall.Unmount(putold, syscall.MNT_DETACH); err != nil {
		return err
	}
	if err := os.RemoveAll(putold); err != nil {
		return err
	}

	return nil
}

func setupVeth(nspid int) (err error) {
	prefix := "vm"
	bridge := "br0"
	hostName := fmt.Sprintf("%s0", prefix)
	containerName := fmt.Sprintf("%s1", prefix)

	attrs := netlink.NewLinkAttrs()
	attrs.Name = hostName
	veth := &netlink.Veth{
		LinkAttrs: attrs,
		PeerName:  containerName,
	}

	if err := netlink.LinkAdd(veth); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			netlink.LinkDel(veth)
		}
	}()

	brl, err := netlink.LinkByName(bridge)
	if err != nil {
		fmt.Printf("get bridge err\n")
		return err
	}
	br, ok := brl.(*netlink.Bridge)
	if !ok {
		fmt.Errorf("Wrong device type %T", brl)
	}

	if err := netlink.LinkSetMaster(veth, br); err != nil {
		return err
	}
	if err := netlink.LinkSetUp(veth); err != nil {
		return err
	}

	cl, err := netlink.LinkByName(containerName)
	if err != nil {
		return err
	}
	if err := netlink.LinkSetNsPid(cl, nspid); err != nil {
		return err
	}
	return nil
}

func setupNetwork() error {
	prefix := "vm"
	containerName := fmt.Sprintf("%s1", prefix)

	cl, err := netlink.LinkByName(containerName)
	if err != nil {
		return err
	}
	ip := &netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP("10.10.10.2"), Mask: net.IPv4Mask(255, 255, 255, 0)}}
	fmt.Printf("IP %s\n", ip)
	if err := netlink.AddrAdd(cl, ip); err != nil {
		return err
	}
	if err := netlink.LinkSetUp(cl); err != nil {
		return err
	}

	gw := net.ParseIP("10.10.10.1")
	if err := netlink.RouteAdd(&netlink.Route{
		Scope:     netlink.SCOPE_UNIVERSE,
		LinkIndex: cl.Attrs().Index,
		Gw:        gw,
	}); err != nil {
		return err
	}
	return nil
}

func waitForNetwork() error {
	maxWait := time.Second * 10
	start := time.Now()
	for {
		interfaces, err := net.Interfaces()
		if err != nil {
			return err
		}
		if len(interfaces) > 1 {
			fmt.Printf("Network setup down...\n")
			return nil
		}

		if time.Since(start) > maxWait {
			return fmt.Errorf("Timeout network setup")
		}

		time.Sleep(time.Second)
	}
}

func main() {

	rootPath := os.Args[1]
	cmd := reexec.Command("nsInitialisation", rootPath)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWNS |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWNET |
			syscall.CLONE_NEWIPC |
			syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{
			{
				ContainerID: 0,
				HostID:      os.Getuid(),
				Size:        1,
			},
		},
		GidMappings: []syscall.SysProcIDMap{
			{
				ContainerID: 0,
				HostID:      os.Getgid(),
				Size:        1,
			},
		},
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("Error running command - %s\n", err)
		os.Exit(1)
	}

	go func() {
		if err := setupVeth(cmd.Process.Pid); err != nil {
			fmt.Printf("Error setup veth, %s\n", err)
		}
	}()

	if err := cmd.Wait(); err != nil {
		fmt.Printf("Error waiting command - %s\n", err)
		os.Exit(1)
	}
}
