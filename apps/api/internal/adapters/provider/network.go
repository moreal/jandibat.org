package provider

import "github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fixedhttp"

type AddressResolver = fixedhttp.AddressResolver
type ContextDialer = fixedhttp.ContextDialer
type HTTPNetwork = fixedhttp.Network
type TransportWrapper = fixedhttp.TransportWrapper

var (
	ErrDialTargetNotAllowed  = fixedhttp.ErrDialTargetNotAllowed
	ErrUnsafeResolvedAddress = fixedhttp.ErrUnsafeResolvedAddress
)
