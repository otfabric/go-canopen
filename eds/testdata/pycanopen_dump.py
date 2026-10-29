# SPDX-License-Identifier: MIT
#
# Dumps the object dictionary python canopen builds from an EDS/DCF file
# as JSON, for the optional cross-check in crosscheck_test.go.
# Usage: python3 pycanopen_dump.py FILE NODEID
import json
import sys

import canopen
from canopen import objectdictionary as odm


def value(v):
    if v is None:
        return None
    if isinstance(v, (bytes, bytearray)):
        return {"hex": bytes(v).hex()}
    if isinstance(v, bool):
        return {"int": int(v)}
    if isinstance(v, int):
        return {"int": v}
    if isinstance(v, float):
        return {"float": v}
    return {"str": str(v)}


def var(v):
    return {
        "index": v.index,
        "sub": v.subindex,
        "name": v.name,
        "type": v.data_type,
        "access": v.access_type,
        "pdo": bool(v.pdo_mappable),
        "relative": bool(getattr(v, "relative", False)),
        "default": value(v.default),
        "value": value(getattr(v, "value", None)),
    }


od = canopen.import_od(sys.argv[1], int(sys.argv[2], 0))
out = []
for index in sorted(od.indices):
    obj = od[index]
    if isinstance(obj, odm.ODVariable):
        out.append(var(obj))
        continue
    for sub in sorted(obj.subindices):
        out.append(var(obj.subindices[sub]))
json.dump(out, sys.stdout)
