// This example shows how to use espradio.Stop() to power down the Wi-Fi driver
// between uses.  It is the pattern for battery-powered applications that only
// need the network occasionally, e.g. a clock: every syncInterval the radio
// starts, the clock syncs with NTP over DHCP + DNS, and the radio stops again.
// On an ESP32 this drops roughly 50 mA of system current while the radio is down.
//
// Note that Stop() does not undo Enable(): init-time state survives, but the
// AP association and the DHCP lease do not.  Each cycle therefore reconnects
// and runs DHCP from scratch.
//
// tinygo flash -target xiao-esp32c3 -ldflags="-X main.ssid=YourSSID -X main.password=YourPassword" -monitor ./examples/ntp-power-save
package main

import (
	"net/netip"
	"runtime"
	"time"

	"github.com/soypat/lneto"
	"tinygo.org/x/espradio"
)

var (
	ssid     string
	password string
)

const (
	ntpHost      = "pool.ntp.org"
	syncInterval = time.Minute
	pollTime     = 5 * time.Millisecond
)

var pollBackoff = lneto.BackoffStrategy(func(_ uint) time.Duration {
	return pollTime
})

func main() {
	time.Sleep(time.Second)

	println("initializing radio...")
	// Enable once; Stop() does not undo it, so every cycle only needs Start()
	// to bring the radio back.
	err := espradio.Enable(espradio.Config{
		Logging: espradio.LogLevelError,
	})
	if err != nil {
		failure("could not enable radio: " + err.Error())
	}

	for cycle := 1; ; cycle++ {
		println("--- sync cycle", cycle, "---")
		syncTime()
		println("radio is down; sleeping", syncInterval.String(), "until next sync")
		time.Sleep(syncInterval)
	}
}

// syncTime brings the radio up, syncs the clock with NTP, then powers the
// radio back down.  A failed cycle is reported on serial; the next cycle
// simply retries.
func syncTime() {
	println("starting radio...")
	if err := espradio.Start(); err != nil {
		println("start failed:", err.Error())
		return
	}

	syncClock()

	println("stopping radio...")
	if err := espradio.Stop(); err != nil {
		println("stop failed:", err.Error())
		return
	}
	println("radio stopped.")
}

// syncClock connects to the AP, gets an IP address with DHCP and syncs the
// clock with NTP.  The poll loop it starts is retired before it returns, so
// nothing is left running while the radio is down.
func syncClock() {
	println("connecting to", ssid, "...")
	err := espradio.Connect(espradio.STAConfig{
		SSID:     ssid,
		Password: password,
	})
	if err != nil {
		println("connect failed:", err.Error())
		return
	}
	println("connected to", ssid, "!")

	println("starting L2 netdev...")
	nd, err := espradio.StartNetDev()
	if err != nil {
		println("netdev failed:", err.Error())
		return
	}

	println("creating lneto stack...")
	stack, err := espradio.NewStack(nd, espradio.StackConfig{
		Hostname:    ssid,
		MaxUDPPorts: 2, // DNS + NTP
		MaxTCPPorts: 1,
	})
	if err != nil {
		println("stack failed:", err.Error())
		return
	}

	// Poll the stack in the background while the network is up.
	done := make(chan struct{})
	go stackLoop(stack, done)
	defer func() {
		close(done)
		// Let the poll loop finish any in-flight send before the radio goes
		// down underneath it.
		time.Sleep(100 * time.Millisecond)
	}()

	println("starting DHCP...")
	dhcp, err := stack.SetupWithDHCP(espradio.DHCPConfig{})
	if err != nil {
		println("DHCP failed:", err.Error())
		return
	}
	addr, ok := netip.AddrFromSlice(dhcp.AssignedAddr4[:])
	if !ok {
		println("invalid IP address")
		return
	}
	println("got IP:", addr.String())

	ntpSync(stack)
}

// ntpSync looks up the NTP host with DNS and queries it for the current time,
// adjusting the runtime clock with the measured offset.
func ntpSync(stack *espradio.Stack) {
	println("resolving ntp host:", ntpHost)
	rstack := stack.LnetoStack().StackRetrying(pollBackoff)

	addrs, err := rstack.DoLookupIP(ntpHost, 5*time.Second, 3)
	if err != nil {
		println("DNS lookup failed:", err.Error())
		return
	}

	offset, err := rstack.DoNTP(addrs[0], 5*time.Second, 3)
	if err != nil {
		println("NTP query failed:", err.Error())
		return
	}

	runtime.AdjustTimeOffset(int64(offset))
	println("NTP success:", time.Now().String())
}

func stackLoop(stack *espradio.Stack, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		default:
		}
		send, recv, err := stack.RecvAndSend()
		if send == 0 && recv == 0 {
			time.Sleep(pollTime)
		}
		if err != nil {
			println("poll err:", err.Error())
		}
	}
}

func failure(msg string) {
	for {
		println("failure:", msg)
		time.Sleep(1 * time.Second)
	}
}
