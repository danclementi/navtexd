//go:build !windows

package capture

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type proc struct {
	cmd *exec.Cmd
	out io.ReadCloser
}

func (p *proc) Read(b []byte) (int, error) { return p.out.Read(b) }
func (p *proc) Close() error {
	p.out.Close()
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	p.cmd.Wait()
	return nil
}

func open(name string, rate float64, channels int) (ReadCloser, error) {
	var args []string
	var tool string
	if path, err := exec.LookPath("parec"); err == nil {
		tool = path
		args = []string{"--format=s16le", fmt.Sprintf("--rate=%d", int(rate)), fmt.Sprintf("--channels=%d", channels), "--latency-msec=50"}
		if name != "" && name != "default" {
			args = append(args, "--device="+name)
		}
	} else if path, err := exec.LookPath("arecord"); err == nil {
		tool = path
		args = []string{"-q", "-t", "raw", "-f", "S16_LE", "-r", fmt.Sprint(int(rate)), "-c", fmt.Sprint(channels)}
		if name != "" && name != "default" {
			args = append(args, "-D", name)
		}
	} else {
		return nil, errors.New("live capture needs parec (PulseAudio/PipeWire) or arecord (ALSA) on the PATH; " +
			"alternatively pipe raw audio into `navtex -set input.source=stdin`")
	}
	cmd := exec.Command(tool, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &proc{cmd: cmd, out: out}, nil
}

func list() ([]Device, error) {
	var devs []Device
	if path, err := exec.LookPath("pactl"); err == nil {
		out, err := exec.Command(path, "list", "short", "sources").Output()
		if err == nil {
			sc := bufio.NewScanner(strings.NewReader(string(out)))
			for sc.Scan() {
				f := strings.Split(sc.Text(), "\t")
				if len(f) >= 2 {
					note := ""
					if strings.HasSuffix(f[1], ".monitor") {
						note = "monitor of an output"
					}
					devs = append(devs, Device{Name: f[1], Note: note})
				}
			}
			return devs, nil
		}
	}
	if path, err := exec.LookPath("arecord"); err == nil {
		out, err := exec.Command(path, "-L").Output()
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(string(out), "\n") {
			if l != "" && l[0] != ' ' && l[0] != '\t' {
				devs = append(devs, Device{Name: l})
			}
		}
		return devs, nil
	}
	return nil, errors.New("neither pactl nor arecord found")
}
