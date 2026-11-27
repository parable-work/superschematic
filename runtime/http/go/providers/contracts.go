package providers

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// Interface is one of the runtime interfaces (D159): "credentials",
// "channel", "sandbox", "model" or "files".
type Interface string

// The runtime interfaces, ir's names.
const (
	Credentials Interface = ir.RuntimeCredentials
	Channel     Interface = ir.RuntimeChannel
	Sandbox     Interface = ir.RuntimeSandbox
	Model       Interface = ir.RuntimeModel
	Files       Interface = ir.RuntimeFiles
)

// Contract is what a provider of one interface implements: the
// operations core calls, each one it must and each one it may.
type Contract = ir.RuntimeContract

// Contracts are the five contracts, in ir's order, which
// runtime_contracts.json holds.
func Contracts() []Contract {
	out := make([]Contract, len(ir.RuntimeContracts))
	copy(out, ir.RuntimeContracts)
	return out
}

// ContractOf is the contract of the interface named name, and false for a
// name that is none.
func ContractOf(name Interface) (Contract, bool) {
	return ir.RuntimeContractOf(string(name))
}

// title is what a message calls a provider of iface.
func (iface Interface) title() string {
	if c, ok := ContractOf(iface); ok {
		return c.Title
	}
	return fmt.Sprintf("provider of %q", string(iface))
}

// ExtensionSubject is the subject of a provider's principal:
// extension:<name>.
func ExtensionSubject(provider string) string { return ir.ExtensionSubject(provider) }

// ConfigField is the config field a provider in slot reads its setting
// key from: SMS and URL give SMS_URL.
func ConfigField(slot, key string) string { return ir.ProviderConfigField(slot, key) }
