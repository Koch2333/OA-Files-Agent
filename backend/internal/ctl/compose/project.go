package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"oa-agent/backend/internal/ctl/install"
)

// Project is docker compose bound to one installation.
type Project struct {
	R    Runner
	Inst *install.Install
	// Poll is the interval used by WaitHealthy (default 3s; tests shrink it).
	Poll time.Duration
}

// Spec builds a `docker compose ...` call in the install dir with COMPOSE_FILE/PROFILES from .env.
func (p *Project) Spec(args ...string) Spec {
	return Spec{
		Dir:  p.Inst.Dir,
		Args: append([]string{"compose"}, args...),
		Env: []string{
			"COMPOSE_FILE=" + p.Inst.ComposeFile(),
			"COMPOSE_PROFILES=" + p.Inst.Profiles(),
		},
	}
}

// Run executes a compose subcommand and captures output.
func (p *Project) Run(ctx context.Context, args ...string) (Result, error) {
	return p.R.Run(ctx, p.Spec(args...))
}

// Stream executes a compose subcommand line by line.
func (p *Project) Stream(ctx context.Context, line func(string), args ...string) error {
	return p.R.Stream(ctx, p.Spec(args...), line)
}

// Service is one row of `docker compose ps`.
type Service struct {
	Name   string
	State  string // running, exited, ...
	Health string // healthy, unhealthy, starting or ""
	Status string // "Up 3 hours (healthy)"
	Image  string
	Ports  string
}

// Effective is the one word shown in tables: health when the image defines a healthcheck, else state.
func (s Service) Effective() string {
	if s.Health != "" {
		return s.Health
	}
	if s.State == "" {
		return "missing"
	}
	return s.State
}

// OK reports whether the service is up (healthy, or running without healthcheck).
func (s Service) OK() bool { e := s.Effective(); return e == "healthy" || e == "running" }

var upRe = regexp.MustCompile(`(?i)^(?:up|running)\s+(.*?)(?:\s*\((?:healthy|unhealthy|health: starting)\))?$`)

// Uptime extracts "3 hours" from the Status column.
func (s Service) Uptime() string {
	if m := upRe.FindStringSubmatch(strings.TrimSpace(s.Status)); m != nil {
		return strings.TrimSpace(m[1])
	}
	return "-"
}

type psRow struct {
	Service    string
	Name       string
	State      string
	Health     string
	Status     string
	Image      string
	Publishers []struct {
		URL           string
		TargetPort    int
		PublishedPort int
		Protocol      string
	}
}

// ParsePS accepts both the NDJSON stream (compose >= 2.21) and the JSON array of older v2 releases.
func ParsePS(out string) ([]Service, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var rows []psRow
	if strings.HasPrefix(out, "[") {
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			return nil, fmt.Errorf("parse compose ps: %w", err)
		}
	} else {
		for _, l := range strings.Split(out, "\n") {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			var r psRow
			if err := json.Unmarshal([]byte(l), &r); err != nil {
				return nil, fmt.Errorf("parse compose ps: %w", err)
			}
			rows = append(rows, r)
		}
	}
	var svcs []Service
	for _, r := range rows {
		name := r.Service
		if name == "" {
			name = r.Name
		}
		seen := map[string]bool{}
		var ports []string
		for _, pb := range r.Publishers {
			if pb.PublishedPort == 0 {
				continue
			}
			k := fmt.Sprintf("%d>%d/%s", pb.PublishedPort, pb.TargetPort, pb.Protocol)
			if seen[k] {
				continue // IPv4 and IPv6 publishers repeat
			}
			seen[k] = true
			host := pb.URL
			if host == "" || host == "::" {
				host = "0.0.0.0"
			}
			ports = append(ports, fmt.Sprintf("%s:%d->%d/%s", host, pb.PublishedPort, pb.TargetPort, pb.Protocol))
		}
		svcs = append(svcs, Service{Name: name, State: r.State, Health: r.Health, Status: r.Status, Image: r.Image, Ports: strings.Join(ports, ", ")})
	}
	return svcs, nil
}

// PS lists containers (including stopped ones).
func (p *Project) PS(ctx context.Context) ([]Service, error) {
	res, err := p.Run(ctx, "ps", "-a", "--format", "json")
	if err != nil {
		return nil, err
	}
	return ParsePS(res.Stdout)
}

// Table returns one row per expected service, in stack order, adding "missing" rows.
func (p *Project) Table(ctx context.Context) ([]Service, error) {
	got, err := p.PS(ctx)
	if err != nil {
		return nil, err
	}
	by := map[string]Service{}
	for _, s := range got {
		by[s.Name] = s
	}
	var out []Service
	for _, n := range p.Inst.Services() {
		s, ok := by[n]
		if !ok {
			s = Service{Name: n}
		}
		out = append(out, s)
		delete(by, n)
	}
	var rest []string
	for n := range by {
		rest = append(rest, n)
	}
	sort.Strings(rest)
	for _, n := range rest {
		out = append(out, by[n])
	}
	return out, nil
}

// Running lists the names of services that are currently running.
func (p *Project) Running(ctx context.Context) ([]string, error) {
	got, err := p.PS(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range got {
		if s.State == "running" || s.State == "restarting" {
			out = append(out, s.Name)
		}
	}
	return out, nil
}

// WaitHealthy polls until every named service is up. A service that is unhealthy/exited fails fast.
func (p *Project) WaitHealthy(ctx context.Context, services []string, timeout time.Duration) error {
	poll := p.Poll
	if poll <= 0 {
		poll = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		got, err := p.PS(ctx)
		if err == nil {
			by := map[string]Service{}
			for _, s := range got {
				by[s.Name] = s
			}
			all := true
			for _, n := range services {
				s := by[n]
				switch s.Effective() {
				case "healthy", "running":
				case "unhealthy", "exited", "dead":
					return fmt.Errorf("service %s is %s", n, s.Effective())
				default:
					all = false
				}
			}
			if all {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %s to become healthy", strings.Join(services, ", "))
		case <-time.After(poll):
		}
	}
}
