// SPDX-License-Identifier: MIT

//go:build interop

package interop

// knownDifferences are the documented differences (INTEROPERABILITY.md):
// a scenario that observes one ends as known-difference with the reason,
// never as a pass. Each was checked on the bus (candump) or in the stack's
// source before it was listed; differences that were go-canopen bugs were
// fixed instead (see CHANGELOG.md).
var knownDifferences = []knownDifference{
	// go-canopen as master/client, the stack's device.
	{dirClient, "canopennode", "*upload-0x1003:0[1-8]", "",
		"CANopenNode's SDO server aborts the upload of a pre-defined error field entry above the number of errors (0x1003:00) with 0x08000024 (no data available); the EDS default 0 is not readable"},
	{dirClient, "lely", "*upload-0x1003:0[1-8]", "",
		"Lely's SDO server aborts the upload of a pre-defined error field entry above the number of errors (0x1003:00) with 0x08000024 (no data available); the EDS default 0 is not readable"},
	{dirClient, "canopennode", "*upload-0x2005:00", "",
		"CANopenNode's SDO server cannot upload 0 bytes: the DOMAIN 0x2005 is empty until written, and the read is aborted with 0x06040047 (CO_SDOserver.c); canopen-interop known difference"},
	{dirClient, "pythoncanopen", "upload-0x2005:00", "",
		"python canopen's LocalNode aborts the upload of a DOMAIN that was never written (no default) with 0x060A0023; canopen-interop known difference"},
	{dirClient, "canopennode", "*domain-0", "",
		"CANopenNode's SDO server refuses a download of 0 bytes into the DOMAIN (segmented: 0x06070013, block: 0x06040047) and cannot upload 0 bytes"},
	{dirClient, "pythoncanopen", "domain-0", "",
		"python canopen's LocalNode stores the 0 bytes written but aborts their upload with 0x08000024 (no data available)"},
	{dirClient, "pythoncanopen", "abort-no-subindex", "",
		"python canopen's LocalNode ignores the sub-index of a VAR object: an upload of 0x2000:01 returns 0x2000:00 instead of aborting with 0x06090011; canopen-interop known difference"},
	{dirClient, "canopennode", "device-dump", "nul-terminated",
		"CANopenNode keeps a VISIBLE_STRING in a fixed buffer of the EDS default's length: a shorter string written by SDO is NUL-terminated and the canopennode adapter's --dump prints the whole buffer (the SDO upload returns the string without the padding)"},
	{dirClient, "canopennode", "configure-from-dcf", "nul-terminated",
		"CANopenNode keeps a VISIBLE_STRING in a fixed buffer of the EDS default's length: a shorter string written by SDO is NUL-terminated and the canopennode adapter's --dump prints the whole buffer (the SDO upload returns the string without the padding)"},

	// Each stack as master/client, the go-canopen node.
	{dirDevice, "lely", "block-domain-0", "",
		"Lely's SDO client (coapp Sdo) reports a block download of 0 bytes as done without sending any frame: the node's DOMAIN keeps its previous value"},

	// EDS checkers.
	{dirEDS, "lely", "valid-object-links.eds", "",
		"Lely's dcf-tools do not know the CiA 306 [<index>ObjectLinks] section (\"unknown section in DCF\") and lint fails; go-canopen and python canopen accept it; canopen-interop known difference"},
	{dirEDS, "lely", "valid-compact.eds", "",
		"Lely's dcf-tools reject the octal literal 0755 (CiA 306 allows octal) and do not support the data type definition objects 0x0020/0x0040; go-canopen reads both"},
	{dirEDS, "lely", "valid-io401.eds", "",
		"Lely's dcf-tools reject the node-ID formula written number first (0x280+$NODEID) and the REAL32 LowLimit 0.0; go-canopen and python canopen accept both"},
	{dirEDS, "lely", "valid-node37.dcf", "",
		"Lely's dcf-tools reject the node-ID formula written number first (0x280+$NODEID) and the REAL32 LowLimit 0.0; go-canopen and python canopen accept both"},
	{dirEDS, "*", "valid-drive402.eds", "",
		"The file starts with a UTF-8 byte order mark: Python's configparser (used by Lely's dcf-tools and python canopen) reports \"no section headers\"; go-canopen skips the mark"},
	{dirEDS, "pythoncanopen", "valid-compact.eds", "",
		"python canopen's import_od does not support the data type definition objects 0x0020/0x0040 (KeyError); go-canopen reads them"},
	{dirEDS, "pythoncanopen", "valid-python_canopen_sample.eds", "",
		"python canopen's import_od does not validate: it accepts its own sample, in which go-canopen's eds.Validate reports SubNumber counts that do not match the sub-object sections, an object list count mismatch and the unknown data type 0x0040 (Lely's dcf-tools reject the file too)"},
	{dirEDS, "pythoncanopen", "valid-canopennode_ds301_profile.eds", "",
		"python canopen's import_od fails on an empty DefaultValue of an integer sub-object (int('')); go-canopen reads it as 0 and Lely's dcf-tools accept the file"},
	{dirEDS, "pythoncanopen", "objects-compact-subobj.eds", "",
		"CompactSubObj: python canopen creates sub 0 as rw and only sub 1 (further sub-objects on access); go-canopen and Lely create sub 0 ro and sub-objects 1..n; canopen-interop known difference"},
	{dirEDS, "pythoncanopen", "objects-*", "dummy",
		"python canopen lists the data types of [DummyUsage] (0x0002..0x0007) as objects of the dictionary; go-canopen's dictionary does not hold them (it accepts them as dummy entries in RPDO mappings)"},
}
