package tunnel

// OperatorResolvers maps a SIM's MCC+MNC to resolvers its operator runs. They
// usually answer their own subscribers only, so they are added only for a
// matching SIM. The network's own resolver (Config.SystemResolvers) covers
// any operator missing here.
var OperatorResolvers = map[string][]string{
	// MTS.
	"25001": {
		"213.87.2.88:53", "213.87.2.89:53", "195.34.32.116:53", "212.188.4.10:53",
		"213.87.0.1:53", "213.87.1.1:53", "217.66.16.35:53",
	},
	// MegaFon, Yota.
	"25002": {"178.176.63.46:53"},
	"25011": {"178.176.63.46:53"},
}
