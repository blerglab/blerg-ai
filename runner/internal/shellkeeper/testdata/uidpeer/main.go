// uidpeer is a disposable different-UID socket fixture, never an installed tool.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func main() {
	os.Exit(entry())
}
func entry() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "uid fixture failed:", err)
		return 1
	}
	return 0
}
func run(ctx context.Context) error {
	switch os.Args[1] {
	case "client":
		if os.Getuid() != 65534 {
			return fmt.Errorf("client UID not changed")
		}
		c, err := (&net.Dialer{}).DialContext(ctx, "unix", "/fixture/keeper.sock")
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		data, err := os.ReadFile("/fixture/request.json")
		if err != nil {
			return err
		}
		if _, err = c.Write(append(data, '\n')); err != nil {
			if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
				fmt.Println("refused")
				return nil
			}
			return err
		}
		line, err := bufio.NewReader(c).ReadString('\n')
		if len(line) > 0 {
			fmt.Println("accepted")
			return nil
		}
		if !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
			return err
		}
		fmt.Println("refused")
		return nil
	case "server":
		owner, err := strconv.Atoi(os.Args[2])
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, "/peer", "leaf")
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534, NoSetGroups: true}}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		cmd.Stderr = os.Stderr
		if err = cmd.Start(); err != nil {
			return err
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		reader := bufio.NewReader(out)
		if line, err := reader.ReadString('\n'); err != nil || line != "bound\n" {
			return fmt.Errorf("bind: %w", err)
		}
		if err = os.Chown("/fixture/keeper.sock", owner, -1); err != nil {
			return err
		}
		fmt.Println("ready")
		if _, err = io.Copy(os.Stdout, reader); err != nil {
			return err
		}
		return cmd.Wait()
	case "leaf":
		if os.Getuid() != 65534 {
			return fmt.Errorf("server UID not changed")
		}
		l, err := (&net.ListenConfig{}).Listen(ctx, "unix", "/fixture/keeper.sock")
		if err != nil {
			return err
		}
		defer func() { _ = l.Close() }()
		if err = os.Chmod("/fixture/keeper.sock", 0600); err != nil {
			return err
		}
		fmt.Println("bound")
		c, err := l.Accept()
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var b [1]byte
		n, err := c.Read(b[:])
		if n > 0 {
			fmt.Println("accepted")
			return nil
		}
		if !errors.Is(err, io.EOF) {
			return err
		}
		fmt.Println("refused")
		return nil
	}
	return fmt.Errorf("unknown mode")
}
