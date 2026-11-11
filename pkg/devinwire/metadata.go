package devinwire

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	ClientName    = "chisel"
	ClientVersion = "3000.10.21"

	fingerprintHexLength = 732
)

// Metadata encodes the client identity shared by Devin's chat and unary RPCs.
func Metadata(token string) []byte {
	var fingerprint [fingerprintHexLength / 2]byte
	var deviceID string
	if _, err := rand.Read(fingerprint[:]); err == nil {
		deviceID = hex.EncodeToString(fingerprint[:])
	} else {
		seed := uuid.NewString()
		var builder strings.Builder
		for counter := 0; builder.Len() < fingerprintHexLength; counter++ {
			hash := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", seed, counter)))
			builder.WriteString(hex.EncodeToString(hash[:]))
		}
		deviceID = builder.String()[:fingerprintHexLength]
	}

	var metadata []byte
	for _, field := range []struct {
		number protowire.Number
		value  string
	}{
		{1, ClientName},
		{2, ClientVersion},
		{3, token},
		{4, "en"},
		{5, runtime.GOOS},
		{7, ClientVersion},
		{12, ClientName},
		{31, deviceID},
	} {
		metadata = protowire.AppendTag(metadata, field.number, protowire.BytesType)
		metadata = protowire.AppendString(metadata, field.value)
	}
	return metadata
}
