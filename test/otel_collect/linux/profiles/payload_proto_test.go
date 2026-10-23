// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package profiles

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func protoValues(t *testing.T, msg []byte, field protowire.Number) [][]byte {
	t.Helper()
	var values [][]byte
	for len(msg) > 0 {
		num, typ, n := protowire.ConsumeTag(msg)
		require.GreaterOrEqual(t, n, 0, "bad tag")
		msg = msg[n:]
		m := protowire.ConsumeFieldValue(num, typ, msg)
		require.GreaterOrEqual(t, m, 0, "bad value of field %d", num)
		if num == field {
			v := msg[:m]
			if typ == protowire.BytesType {
				v, _ = protowire.ConsumeBytes(v)
			}
			values = append(values, v)
		}
		msg = msg[m:]
	}
	return values
}

func protoValue(t *testing.T, msg []byte, path ...protowire.Number) []byte {
	t.Helper()
	for _, field := range path {
		values := protoValues(t, msg, field)
		require.Len(t, values, 1, "field %d", field)
		msg = values[0]
	}
	return msg
}

func resourceAttrKeys(t *testing.T, req []byte) map[string]string {
	t.Helper()
	resource := protoValue(t, req, 1, 1)
	attrs := map[string]string{}
	for _, kv := range protoValues(t, resource, 1) {
		key := string(protoValue(t, kv, 1))
		attrs[key] = string(protoValue(t, kv, 2, 1))
	}
	return attrs
}

func TestBuildProfilesProtoServiceName(t *testing.T) {
	withName := resourceAttrKeys(t, buildProfilesProto("svc-1", "i-0"))
	require.Equal(t, map[string]string{serviceNameKey: "svc-1", "instance_id": "i-0"}, withName)

	withoutName := resourceAttrKeys(t, buildProfilesProto("", "i-0"))
	require.Equal(t, map[string]string{"instance_id": "i-0"}, withoutName)
}

func TestBuildProfilesProtoProfileID(t *testing.T) {
	req := buildProfilesProto("svc", "i-0")
	profile := protoValue(t, req, 1, 2, 2)
	id := protoValue(t, profile, 7)
	require.Len(t, id, 16)

	ts, n := protowire.ConsumeFixed64(protoValue(t, profile, 3))
	require.Equal(t, 8, n)
	require.Equal(t, profileIDBytes(ts), id)

	fromJSON, err := hex.DecodeString(profileIDJSON(ts))
	require.NoError(t, err)
	require.Equal(t, fromJSON, id)
}

func TestBuildProfilesProtoDictionary(t *testing.T) {
	dict := protoValue(t, buildProfilesProto("svc", "i-0"), 2)

	var stringsGot []string
	for _, s := range protoValues(t, dict, 5) {
		stringsGot = append(stringsGot, string(s))
	}
	require.Equal(t, payloadStringTable, stringsGot)

	functions := protoValues(t, dict, 3)
	require.Len(t, functions, 3)
	require.Empty(t, functions[0], "function_table[0] must be the empty entry")
	for i, want := range []string{"testFunctionA", "testFunctionB"} {
		nameIndex, _ := protowire.ConsumeVarint(protoValue(t, functions[i+1], 1))
		require.Equal(t, want, payloadStringTable[nameIndex])
	}

	stacks := protoValues(t, dict, 7)
	require.Len(t, stacks, 2)
	require.Len(t, protoValues(t, dict, 2), 3, "location_table")
	require.Equal(t, []byte{1, 2}, protoValue(t, stacks[1], 1), "packed location_indices")
}

const goldenProfilesRequest = "" +
	"0a9e010a2f0a170a0c736572766963652e6e616d6512070a057376632d310a140a0b696e7374616e" +
	"63655f696412050a03692d30126b0a240a1b636c6f756477617463682d6167656e742d696e746567" +
	"2d746573741205312e302e3012430a0408011002120f08012201642a0800002a36fe9c9717190000" +
	"2a36fe9c97172080c8afa0252a04080310043080ade2043a10000000000000000117979cfe362a00" +
	"00127d0a00120012091080201a040801100a12091080401a04080210141a001a04080518071a0408" +
	"06180722002a002a0773616d706c65732a05636f756e742a036370752a0b6e616e6f7365636f6e64" +
	"732a0d7465737446756e6374696f6e412a0d7465737446756e6374696f6e422a07746573742e676f" +
	"3a003a040a020102"

func TestEncodeProfilesRequestGolden(t *testing.T) {
	want, err := hex.DecodeString(goldenProfilesRequest)
	require.NoError(t, err)
	got := encodeProfilesRequest([]resourceAttr{attr(serviceNameKey, "svc-1"), attr("instance_id", "i-0")}, 1700000000000000000)
	require.True(t, bytes.Equal(want, got), "encoded request drifted from the golden bytes:\nwant %x\ngot  %x", want, got)
}

func TestEncodeProfilesRequestProfileAttributes(t *testing.T) {
	oversized := resourceProfile{
		resource:     []resourceAttr{attr(serviceNameKey, "svc-oversized"), attr("instance_id", "i-0")},
		profileAttrs: []resourceAttr{attr("integ.test.attr.000", "x"), attr("integ.test.attr.001", "y")},
	}
	valid := resourceProfile{resource: []resourceAttr{attr(serviceNameKey, "svc"), attr("instance_id", "i-0")}}
	req := encodeProfilesRequestWithAttrs([]resourceProfile{valid, oversized}, 1700000000000000000)

	resources := protoValues(t, req, 1)
	require.Len(t, resources, 2)
	require.Empty(t, protoValues(t, protoValue(t, resources[0], 2, 2), 11), "the valid profile carries no attribute_indices")
	require.Len(t, protoValues(t, protoValue(t, resources[1], 1), 1), 2, "over-cap attributes must not sit on the resource")

	packed := protoValue(t, resources[1], 2, 2, 11)
	var indices []uint64
	for len(packed) > 0 {
		v, n := protowire.ConsumeVarint(packed)
		require.Greater(t, n, 0)
		indices = append(indices, v)
		packed = packed[n:]
	}
	require.Equal(t, []uint64{1, 2}, indices)

	dict := protoValue(t, req, 2)
	var stringTable []string
	for _, s := range protoValues(t, dict, 5) {
		stringTable = append(stringTable, string(s))
	}
	require.Equal(t, append(append([]string(nil), payloadStringTable...), "integ.test.attr.000", "integ.test.attr.001"), stringTable)

	table := protoValues(t, dict, 6)
	require.Len(t, table, 3)
	require.Empty(t, table[0], "attribute_table[0] must be the empty entry")
	for i, want := range []struct{ key, value string }{{"integ.test.attr.000", "x"}, {"integ.test.attr.001", "y"}} {
		keyIndex, _ := protowire.ConsumeVarint(protoValue(t, table[i+1], 1))
		require.Equal(t, want.key, stringTable[keyIndex])
		require.Equal(t, want.value, string(protoValue(t, table[i+1], 2, 1)))
	}
}
