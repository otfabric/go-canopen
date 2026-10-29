// SPDX-License-Identifier: MIT

// Package eds reads, validates and writes CANopen electronic data sheets
// (EDS) and device configuration files (DCF) as CiA 306-1 specifies them,
// and converts them to and from an [od.Dictionary].
//
// # Model
//
// [Parse] keeps the whole file. The standard sections ([FileInfo],
// [DeviceInfo], [DeviceComissioning], [DummyUsage], [Comments], the three
// object lists) and the object sections ([XXXX], [XXXXsubY], [XXXXName],
// [XXXXValue], [XXXXDenotation], [XXXXObjectLinks]) are interpreted into
// typed fields; every other section (tools sections, modular-device
// sections, vendor extensions) is kept verbatim in [File.Unknown], and
// every key a section does not define, or whose value does not parse as
// the key's type, is kept verbatim in the section's Extra list. Value
// literals whose meaning depends on the data type (DefaultValue,
// LowLimit, HighLimit, ParameterValue) are kept as raw strings and
// evaluated only when a dictionary is built.
//
// Key and section names are case-insensitive. Lines may end in CRLF or LF,
// a UTF-8 byte order mark is skipped, lines whose first non-blank
// character is ';' are comments, and integers are decimal, 0x hexadecimal
// or octal with a leading 0. Text is kept as the bytes of the file (EDS
// files from Windows tools are often Latin-1).
//
// Parse fails only on lines it cannot read (a key outside a section, a
// line that is neither a section header, a key=value pair nor a comment).
// Everything else, from a missing mandatory object to a default value that
// does not fit its type, is reported by [File.Validate].
//
// # Round trip
//
// [Write] writes the model in a canonical, deterministic layout. Parsing
// what Write wrote gives the same model, so parse, write and parse again
// loses no information the model holds. Comments, blank lines, key order
// inside standard sections, the spelling of numbers and duplicate keys
// (the last one wins) are not kept.
//
// # Dictionaries
//
// [File.Dictionary] builds an [od.Dictionary] from the object sections,
// using ParameterValue when present (DCF) and DefaultValue otherwise. A
// "$NODEID+offset" value becomes a node-ID-relative default
// ([od.Variable.NodeIDRelative] with the offset as Default), so the
// dictionary evaluates it for its node-ID and re-evaluates it after
// [od.Dictionary.SetNodeID] and a reset. [FromDictionary] goes the other
// way, for exporting the description or the current configuration of a
// node.
package eds
