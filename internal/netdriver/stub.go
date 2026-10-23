//go:build !linux

package netdriver

import "context"

// stub 在非 Linux 平台提供空实现，保证开发与交叉编译可用。
type stub struct{}

// NewStub 返回空实现的 netdriver。
func NewStub() NetDriver { return &stub{} }

func (s *stub) Probe() Capabilities { return Capabilities{} }

func (s *stub) EnsureLink(ctx context.Context, name string, mtu int) error { return nil }
func (s *stub) DeleteLink(ctx context.Context, name string) error          { return nil }
func (s *stub) SetLinkUp(ctx context.Context, name string) error           { return nil }
func (s *stub) EnsureAddress(ctx context.Context, name, cidr string) error { return nil }
func (s *stub) SetMTU(ctx context.Context, name string, mtu int) error     { return nil }
func (s *stub) EnsureForwarding() error                                    { return nil }
func (s *stub) EnsureNAT(ctx context.Context, name, subnet string) error   { return ErrNATUnavailable }
func (s *stub) RemoveNAT(ctx context.Context, name, subnet string) error   { return nil }

func (s *stub) LinkInfo(name string) (LinkInfo, error) { return LinkInfo{Name: name}, nil }
func (s *stub) EnsureRouting(ctx context.Context, name, subnet, table string, fwmark int) error {
	return nil
}

func (s *stub) ApplyRateLimits(ctx context.Context, dev string, limits []RateLimit) error {
	return ErrRateLimitUnsupported
}
func (s *stub) RateLimitApplied(dev string) bool                      { return false }
func (s *stub) CleanRateLimits(ctx context.Context, dev string) error { return ErrRateLimitUnsupported }

var _ NetDriver = (*stub)(nil)
