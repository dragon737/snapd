// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2014-2015 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package coreconfig

import (
	"errors"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"launchpad.net/snappy/helpers"

	"gopkg.in/yaml.v2"
)

const (
	tzPathEnvironment string = "UBUNTU_CORE_CONFIG_TZ_FILE"
	tzPathDefault     string = "/etc/timezone"
)

var (
	tzZoneInfoPath   = "/usr/share/zoneinfo"
	tzZoneInfoTarget = "/etc/localtime"
)

const (
	autopilotTimer         string = "snappy-autopilot.timer"
	autopilotTimerEnabled  string = "enabled"
	autopilotTimerDisabled string = "disabled"
)

var (
	modprobePath        = "/etc/modprobe.d/ubuntu-core.conf"
	interfacesRoot      = "/etc/network/interfaces.d/"
	pppRoot             = "/etc/ppp/"
	watchdogConfigPath  = "/etc/watchdog.conf"
	watchdogStartupPath = "/etc/default/watchdog"
)

// ErrInvalidUnitStatus signals that a unit is not returning a status
// of "enabled" or "disabled".
var ErrInvalidUnitStatus = errors.New("invalid unit status")

type systemConfig struct {
	Autopilot *bool           `yaml:"autopilot,omitempty"`
	Timezone  *string         `yaml:"timezone,omitempty"`
	Hostname  *string         `yaml:"hostname,omitempty"`
	Modprobe  *string         `yaml:"modprobe,omitempty"`
	Network   *networkConfig  `yaml:"network,omitempty"`
	Watchdog  *watchdogConfig `yaml:"watchdog,omitempty"`
}

type networkConfig struct {
	Interfaces []passthroughConfig `yaml:"interfaces"`
	PPP        []passthroughConfig `yaml:"ppp"`
}

type passthroughConfig struct {
	Name    string `yaml:"name,omitempty"`
	Content string `yaml:"content,omitempty"`
}

type watchdogConfig struct {
	Startup string `yaml:"startup,omitempty"`
	Config  string `yaml:"config,omitempty"`
}

type coreConfig struct {
	UbuntuCore *systemConfig `yaml:"ubuntu-core"`
}

type configYaml struct {
	Config coreConfig
}

func newSystemConfig() (*systemConfig, error) {
	// TODO think of a smart way not to miss a config entry
	tz, err := getTimezone()
	if err != nil {
		return nil, err
	}

	autopilot, err := getAutopilot()
	if err != nil {
		return nil, err
	}
	hostname, err := getHostname()
	if err != nil {
		return nil, err
	}
	modprobe, err := getModprobe()
	if err != nil {
		return nil, err
	}
	interfaces, err := getInterfaces()
	if err != nil {
		return nil, err
	}
	ppp, err := getPPP()
	if err != nil {
		return nil, err
	}
	watchdog, err := getWatchdog()
	if err != nil {
		return nil, err
	}

	var network *networkConfig
	if len(interfaces) > 0 || len(ppp) > 0 {
		network = &networkConfig{
			Interfaces: interfaces,
			PPP:        ppp,
		}
	}

	config := &systemConfig{
		Autopilot: &autopilot,
		Timezone:  &tz,
		Hostname:  &hostname,
		Modprobe:  &modprobe,
		Network:   network,
		Watchdog:  watchdog,
	}

	return config, nil
}

// for testing purposes
var yamlMarshal = yaml.Marshal

// Get is a special configuration case for the system, for which
// there is no such entry in a package.yaml to satisfy the snappy config interface.
// This implements getting the current configuration for ubuntu-core.
func Get() (rawConfig string, err error) {
	config, err := newSystemConfig()
	if err != nil {
		return "", err
	}

	out, err := yamlMarshal(&configYaml{Config: coreConfig{config}})
	if err != nil {
		return "", err
	}

	return string(out), nil
}

func passthroughEqual(a, b []passthroughConfig) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}

	return true
}

// Set is used to configure settings for the system, this is meant to
// be used as an interface for snappy config to satisfy the ubuntu-core
// hook.
func Set(rawConfig string) (newRawConfig string, err error) {
	oldConfig, err := newSystemConfig()
	if err != nil {
		return "", err
	}

	var configWrap configYaml
	err = yaml.Unmarshal([]byte(rawConfig), &configWrap)
	if err != nil {
		return "", err
	}
	newConfig := configWrap.Config.UbuntuCore

	rNewConfig := reflect.ValueOf(newConfig).Elem()
	rType := rNewConfig.Type()
	for i := 0; i < rNewConfig.NumField(); i++ {
		if rNewConfig.Field(i).IsNil() {
			continue
		}

		switch rType.Field(i).Name {
		case "Timezone":
			if *oldConfig.Timezone == *newConfig.Timezone {
				continue
			}

			if err := setTimezone(*newConfig.Timezone); err != nil {
				return "", err
			}
		case "Autopilot":
			if *oldConfig.Autopilot == *newConfig.Autopilot {
				continue
			}

			if err := setAutopilot(*newConfig.Autopilot); err != nil {
				return "", err
			}
		case "Hostname":
			if *oldConfig.Hostname == *newConfig.Hostname {
				continue
			}

			if err := setHostname(*newConfig.Hostname); err != nil {
				return "", err
			}
		case "Modprobe":
			if *oldConfig.Modprobe == *newConfig.Modprobe {
				continue
			}

			if err := setModprobe(*newConfig.Modprobe); err != nil {
				return "", err
			}
		case "Network":
			if oldConfig.Network == nil || !passthroughEqual(oldConfig.Network.Interfaces, newConfig.Network.Interfaces) {
				if err := setInterfaces(newConfig.Network.Interfaces); err != nil {
					return "", err
				}
			}
			if oldConfig.Network == nil || !passthroughEqual(oldConfig.Network.PPP, newConfig.Network.PPP) {
				if err := setPPP(newConfig.Network.PPP); err != nil {
					return "", err
				}
			}
		case "Watchdog":
			if oldConfig.Watchdog != nil && *oldConfig.Watchdog == *newConfig.Watchdog {
				continue
			}

			if err := setWatchdog(newConfig.Watchdog); err != nil {
				return "", err
			}
		}
	}

	return Get()
}

// tzFile determines which timezone file to read from
func tzFile() string {
	tzFile := os.Getenv(tzPathEnvironment)
	if tzFile == "" {
		tzFile = tzPathDefault
	}

	return tzFile
}

// getTimezone returns the current timezone the system is set to or an error
// if it can't.
var getTimezone = func() (timezone string, err error) {
	tz, err := ioutil.ReadFile(tzFile())
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(tz)), nil
}

// setTimezone sets the specified timezone for the system, an error is returned
// if it can't.
var setTimezone = func(timezone string) error {
	if err := helpers.CopyFile(filepath.Join(tzZoneInfoPath, timezone), tzZoneInfoTarget, 0); err != nil {
		return err
	}

	return ioutil.WriteFile(tzFile(), []byte(timezone), 0644)
}

func getPassthrough(rootDir string) (pc []passthroughConfig, err error) {
	filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		content, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}
		pc = append(pc, passthroughConfig{
			Name:    path[len(rootDir):],
			Content: string(content),
		})
		return nil
	})

	return pc, nil

}
func setPassthrough(rootDir string, pc []passthroughConfig) error {
	for _, c := range pc {
		path := filepath.Join(rootDir, c.Name)
		if c.Content == "" {
			os.Remove(path)
			continue
		}
		if err := ioutil.WriteFile(path, []byte(c.Content), 0644); err != nil {
			return err
		}
	}

	return nil
}

var getInterfaces = func() (pc []passthroughConfig, err error) {
	return getPassthrough(interfacesRoot)
}

var setInterfaces = func(pc []passthroughConfig) error {
	return setPassthrough(interfacesRoot, pc)
}

var getPPP = func() (pc []passthroughConfig, err error) {
	return getPassthrough(pppRoot)
}

var setPPP = func(pc []passthroughConfig) error {
	return setPassthrough(pppRoot, pc)
}

// getModprobe returns the current modprobe config
var getModprobe = func() (string, error) {
	modprobe, err := ioutil.ReadFile(modprobePath)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}

	return string(modprobe), nil
}

// setModprobe sets the specified modprobe config
var setModprobe = func(modprobe string) error {
	return ioutil.WriteFile(modprobePath, []byte(modprobe), 0644)
}

// getWatchdog returns the current watchdog config
var getWatchdog = func() (*watchdogConfig, error) {
	startup, err := ioutil.ReadFile(watchdogStartupPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	config, err := ioutil.ReadFile(watchdogConfigPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	// nil to make the yaml empty
	if len(startup) == 0 && len(config) == 0 {
		return nil, nil
	}

	return &watchdogConfig{
		Startup: string(startup),
		Config:  string(config)}, nil
}

// setWatchdog sets the specified watchdog config
var setWatchdog = func(wf *watchdogConfig) error {
	if err := ioutil.WriteFile(watchdogStartupPath, []byte(wf.Startup), 0644); err != nil {
		return err
	}

	return ioutil.WriteFile(watchdogConfigPath, []byte(wf.Config), 0644)
}

// for testing purposes
var (
	cmdAutopilotEnabled = []string{"is-enabled", autopilotTimer}
	cmdSystemctl        = "systemctl"
)

// getAutopilot returns the autopilot state
var getAutopilot = func() (state bool, err error) {
	out, err := exec.Command(cmdSystemctl, cmdAutopilotEnabled...).Output()
	if exitErr, ok := err.(*exec.ExitError); ok {
		waitStatus := exitErr.Sys().(syscall.WaitStatus)

		// when a service is disabled the exit status is 1
		if e := waitStatus.ExitStatus(); e != 1 {
			return false, err
		}
	}

	status := strings.TrimSpace(string(out))

	if status == autopilotTimerEnabled {
		return true, nil
	} else if status == autopilotTimerDisabled {
		return false, nil
	} else {
		return false, ErrInvalidUnitStatus
	}
}

// for testing purposes
var (
	cmdEnableAutopilot  = []string{"enable", autopilotTimer}
	cmdStartAutopilot   = []string{"start", autopilotTimer}
	cmdDisableAutopilot = []string{"disable", autopilotTimer}
	cmdStopAutopilot    = []string{"stop", autopilotTimer}
)

// setAutopilot enables and starts, or stops and disables autopilot
var setAutopilot = func(stateEnabled bool) error {
	if stateEnabled {
		if err := exec.Command(cmdSystemctl, cmdEnableAutopilot...).Run(); err != nil {
			return err
		}
		if err := exec.Command(cmdSystemctl, cmdStartAutopilot...).Run(); err != nil {
			return err
		}
	} else {
		if err := exec.Command(cmdSystemctl, cmdStopAutopilot...).Run(); err != nil {
			return err
		}
		if err := exec.Command(cmdSystemctl, cmdDisableAutopilot...).Run(); err != nil {
			return err
		}
	}

	return nil
}

// getHostname returns the hostname for the host
var getHostname = os.Hostname

var hostnamePath = "/etc/writable/hostname"
var syscallSethostname = syscall.Sethostname

// setHostname sets the hostname for the host
var setHostname = func(hostname string) error {
	hostnameB := []byte(hostname)

	if err := syscallSethostname(hostnameB); err != nil {
		return err
	}

	return ioutil.WriteFile(hostnamePath, hostnameB, 0644)
}
