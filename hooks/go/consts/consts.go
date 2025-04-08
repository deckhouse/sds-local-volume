package consts

const (
	ModuleName		      string = "sdsLocalVolume"
	ModuleNamespace 	  string = "d8-sds-local-volume"
	ModulePluralName      string = "sds-local-volume"
	WebhookCertCn   	  string = "webhooks"
)

var AllowedProvisioners = []string{
	"local.csi.storage.deckhouse.io",
}
