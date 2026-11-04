package verify

// OneWayBooleans and OneWayNumbers are the build's oneWay types, for the
// external tests that hold them to the core's (D104).
var (
	OneWayBooleans = oneWayBooleans
	OneWayNumbers  = oneWayNumbers
)

// HoldTypes, LimitsTypes and IntegerSegments are the build's lists of the
// types Lease's maxHoldField, Retries' limitsField and a Budget meter's
// limitField and reserveField take, for the external tests that hold them
// to the core's (D104).
var (
	HoldTypes       = holdTypes
	LimitsTypes     = limitsTypes
	IntegerSegments = integerSegments
)
