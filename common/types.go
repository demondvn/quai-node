// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package common

import (
	"bytes"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"math/rand"
	"reflect"
	"strconv"
	"strings"

	"github.com/dominant-strategies/go-quai/common/hexutil"
	"golang.org/x/crypto/sha3"
)

// Lengths of hashes and addresses in bytes.
const (
	// HashLength is the expected length of the hash
	HashLength = 32
	// AddressLength is the expected length of the address
	AddressLength = 20

	// Constants to mnemonically index into context arrays
	PRIME_CTX  = 0
	REGION_CTX = 1
	ZONE_CTX   = 2

	// Depth of the hierarchy of chains
	NumRegionsInPrime = 3
	NumZonesInRegion  = 3
	HierarchyDepth    = 3
)

var (
	// Default to prime node, but changed at startup by config.
	NodeLocation = Location{}
)

var (
	hashT    = reflect.TypeOf(Hash{})
	addressT = reflect.TypeOf(Address{})
	// The zero address (0x0)
	ZeroAddr = BytesToAddress([]byte{0})
)

// Hash represents the 32 byte Keccak256 hash of arbitrary data.
type Hash [HashLength]byte

// BytesToHash sets b to hash.
// If b is larger than len(h), b will be cropped from the left.
func BytesToHash(b []byte) Hash {
	var h Hash
	h.SetBytes(b)
	return h
}

// BigToHash sets byte representation of b to hash.
// If b is larger than len(h), b will be cropped from the left.
func BigToHash(b *big.Int) Hash { return BytesToHash(b.Bytes()) }

// HexToHash sets byte representation of s to hash.
// If b is larger than len(h), b will be cropped from the left.
func HexToHash(s string) Hash { return BytesToHash(FromHex(s)) }

// Bytes gets the byte representation of the underlying hash.
func (h Hash) Bytes() []byte { return h[:] }

// Big converts a hash to a big integer.
func (h Hash) Big() *big.Int { return new(big.Int).SetBytes(h[:]) }

// Hex converts a hash to a hex string.
func (h Hash) Hex() string { return hexutil.Encode(h[:]) }

// TerminalString implements log.TerminalStringer, formatting a string for console
// output during logging.
func (h Hash) TerminalString() string {
	return fmt.Sprintf("%x..%x", h[:3], h[29:])
}

// String implements the stringer interface and is used also by the logger when
// doing full logging into a file.
func (h Hash) String() string {
	return h.Hex()
}

// Format implements fmt.Formatter.
// Hash supports the %v, %s, %v, %x, %X and %d format verbs.
func (h Hash) Format(s fmt.State, c rune) {
	hexb := make([]byte, 2+len(h)*2)
	copy(hexb, "0x")
	hex.Encode(hexb[2:], h[:])

	switch c {
	case 'x', 'X':
		if !s.Flag('#') {
			hexb = hexb[2:]
		}
		if c == 'X' {
			hexb = bytes.ToUpper(hexb)
		}
		fallthrough
	case 'v', 's':
		s.Write(hexb)
	case 'q':
		q := []byte{'"'}
		s.Write(q)
		s.Write(hexb)
		s.Write(q)
	case 'd':
		fmt.Fprint(s, ([len(h)]byte)(h))
	default:
		fmt.Fprintf(s, "%%!%c(hash=%x)", c, h)
	}
}

// UnmarshalText parses a hash in hex syntax.
func (h *Hash) UnmarshalText(input []byte) error {
	return hexutil.UnmarshalFixedText("Hash", input, h[:])
}

// UnmarshalJSON parses a hash in hex syntax.
func (h *Hash) UnmarshalJSON(input []byte) error {
	return hexutil.UnmarshalFixedJSON(hashT, input, h[:])
}

// MarshalText returns the hex representation of h.
func (h Hash) MarshalText() ([]byte, error) {
	return hexutil.Bytes(h[:]).MarshalText()
}

// SetBytes sets the hash to the value of b.
// If b is larger than len(h), b will be cropped from the left.
func (h *Hash) SetBytes(b []byte) {
	if len(b) > len(h) {
		b = b[len(b)-HashLength:]
	}

	copy(h[HashLength-len(b):], b)
}

// Generate implements testing/quick.Generator.
func (h Hash) Generate(rand *rand.Rand, size int) reflect.Value {
	m := rand.Intn(len(h))
	for i := len(h) - 1; i > m; i-- {
		h[i] = byte(rand.Uint32())
	}
	return reflect.ValueOf(h)
}

// Scan implements Scanner for database/sql.
func (h *Hash) Scan(src interface{}) error {
	srcB, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("can't scan %T into Hash", src)
	}
	if len(srcB) != HashLength {
		return fmt.Errorf("can't scan []byte of len %d into Hash, want %d", len(srcB), HashLength)
	}
	copy(h[:], srcB)
	return nil
}

// Value implements valuer for database/sql.
func (h Hash) Value() (driver.Value, error) {
	return h[:], nil
}

// UnprefixedHash allows marshaling a Hash without 0x prefix.
type UnprefixedHash Hash

// UnmarshalText decodes the hash from hex. The 0x prefix is optional.
func (h *UnprefixedHash) UnmarshalText(input []byte) error {
	return hexutil.UnmarshalFixedUnprefixedText("UnprefixedHash", input, h[:])
}

// MarshalText encodes the hash as hex.
func (h UnprefixedHash) MarshalText() ([]byte, error) {
	return []byte(hex.EncodeToString(h[:])), nil
}

/////////// Address

type addrPrefixRange struct {
	lo uint8
	hi uint8
}

func NewRange(l, h uint8) addrPrefixRange {
	return addrPrefixRange{
		lo: l,
		hi: h,
	}
}

var (
	locationToPrefixRange = make(map[string]addrPrefixRange)
)

func init() {
	locationToPrefixRange["prime"] = NewRange(0, 9)
	locationToPrefixRange["cyprus"] = NewRange(10, 19)
	locationToPrefixRange["cyprus1"] = NewRange(20, 29)
	locationToPrefixRange["cyprus2"] = NewRange(30, 39)
	locationToPrefixRange["cyprus3"] = NewRange(40, 49)
	locationToPrefixRange["paxos"] = NewRange(50, 59)
	locationToPrefixRange["paxos1"] = NewRange(60, 69)
	locationToPrefixRange["paxos2"] = NewRange(70, 79)
	locationToPrefixRange["paxos3"] = NewRange(80, 89)
	locationToPrefixRange["hydra"] = NewRange(90, 99)
	locationToPrefixRange["hydra1"] = NewRange(100, 109)
	locationToPrefixRange["hydra2"] = NewRange(110, 119)
	locationToPrefixRange["hydra3"] = NewRange(120, 129)
}

// Address represents the 20 byte address of an Ethereum account.
type Address [AddressLength]byte

// BytesToAddress returns Address with value b.
// If b is larger than len(h), b will be cropped from the left.
func BytesToAddress(b []byte) Address {
	var a Address
	a.SetBytes(b)
	return a
}

// BigToAddress returns Address with byte values of b.
// If b is larger than len(h), b will be cropped from the left.
func BigToAddress(b *big.Int) Address { return BytesToAddress(b.Bytes()) }

// HexToAddress returns Address with byte values of s.
// If s is larger than len(h), s will be cropped from the left.
func HexToAddress(s string) Address { return BytesToAddress(FromHex(s)) }

// IsHexAddress verifies whether a string can represent a valid hex-encoded
// Ethereum address or not.
func IsHexAddress(s string) bool {
	if has0xPrefix(s) {
		s = s[2:]
	}
	return len(s) == 2*AddressLength && isHex(s)
}

// Bytes gets the string representation of the underlying address.
func (a Address) Bytes() []byte { return a[:] }

// Hash converts an address to a hash by left-padding it with zeros.
func (a Address) Hash() Hash { return BytesToHash(a[:]) }

// Hex returns an EIP55-compliant hex string representation of the address.
func (a Address) Hex() string {
	return string(a.checksumHex())
}

// String implements fmt.Stringer.
func (a Address) String() string {
	return a.Hex()
}

func (a *Address) checksumHex() []byte {
	buf := a.hex()

	// compute checksum
	sha := sha3.NewLegacyKeccak256()
	sha.Write(buf[2:])
	hash := sha.Sum(nil)
	for i := 2; i < len(buf); i++ {
		hashByte := hash[(i-2)/2]
		if i%2 == 0 {
			hashByte = hashByte >> 4
		} else {
			hashByte &= 0xf
		}
		if buf[i] > '9' && hashByte > 7 {
			buf[i] -= 32
		}
	}
	return buf[:]
}

func (a Address) hex() []byte {
	var buf [len(a)*2 + 2]byte
	copy(buf[:2], "0x")
	hex.Encode(buf[2:], a[:])
	return buf[:]
}

// Format implements fmt.Formatter.
// Address supports the %v, %s, %v, %x, %X and %d format verbs.
func (a Address) Format(s fmt.State, c rune) {
	switch c {
	case 'v', 's':
		s.Write(a.checksumHex())
	case 'q':
		q := []byte{'"'}
		s.Write(q)
		s.Write(a.checksumHex())
		s.Write(q)
	case 'x', 'X':
		// %x disables the checksum.
		hex := a.hex()
		if !s.Flag('#') {
			hex = hex[2:]
		}
		if c == 'X' {
			hex = bytes.ToUpper(hex)
		}
		s.Write(hex)
	case 'd':
		fmt.Fprint(s, ([len(a)]byte)(a))
	default:
		fmt.Fprintf(s, "%%!%c(address=%x)", c, a)
	}
}

// SetBytes sets the address to the value of b.
// If b is larger than len(a), b will be cropped from the left.
func (a *Address) SetBytes(b []byte) {
	if len(b) > len(a) {
		b = b[len(b)-AddressLength:]
	}
	copy(a[AddressLength-len(b):], b)
}

// MarshalText returns the hex representation of a.
func (a Address) MarshalText() ([]byte, error) {
	return hexutil.Bytes(a[:]).MarshalText()
}

// UnmarshalText parses a hash in hex syntax.
func (a *Address) UnmarshalText(input []byte) error {
	return hexutil.UnmarshalFixedText("Address", input, a[:])
}

// UnmarshalJSON parses a hash in hex syntax.
func (a *Address) UnmarshalJSON(input []byte) error {
	return hexutil.UnmarshalFixedJSON(addressT, input, a[:])
}

// Scan implements Scanner for database/sql.
func (a *Address) Scan(src interface{}) error {
	srcB, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("can't scan %T into Address", src)
	}
	if len(srcB) != AddressLength {
		return fmt.Errorf("can't scan []byte of len %d into Address, want %d", len(srcB), AddressLength)
	}
	copy(a[:], srcB)
	return nil
}

// Value implements valuer for database/sql.
func (a Address) Value() (driver.Value, error) {
	return a[:], nil
}

// IsInChainScope checks if an address is a valid account in our node's sharded address space
func (a Address) IsInChainScope() bool {
	if a == ZeroAddr {
		return true
	}
	return NodeLocation.ContainsAddress(a)
}

// Location looks up the chain location which contains this address
func (a Address) Location() *Location {
	R, Z, D := 0, 0, HierarchyDepth
	if NodeLocation.HasRegion() {
		R = NodeLocation.Region()
	}
	if NodeLocation.HasZone() {
		Z = NodeLocation.Zone()
	}

	// Search zone->region->prime address spaces in-slice first, and then search
	// zone->region out-of-slice address spaces next. This minimizes expected
	// search time under the following assumptions:
	// * a node is more likely to encounter a TX from its slice than from another
	// * we expect `>= Z` `zone` TXs for every `region` TX
	// * we expect `>= R` `region` TXs for every `prime` TX
	// * (and by extension) we expect `>= R*Z` `zone` TXs for every `prime` TX
	primeChecked := false
	for r := 0; r < NumRegionsInPrime; r++ {
		for z := 0; z < NumZonesInRegion; z++ {
			l := Location{byte((r+R)%D), byte((z+Z)%D)}
			if l.ContainsAddress(a) {
				return &l
			}
		}
		l := Location{byte((r+R)%D)}
		if l.ContainsAddress(a) {
			return &l
		}
		// Check prime on first pass through slice, but not again
		if !primeChecked {
			primeChecked = true
			l := Location{}
			if l.ContainsAddress(a) {
				return &l
			}
		}
	}
	return nil
}

// UnprefixedAddress allows marshaling an Address without 0x prefix.
type UnprefixedAddress Address

// UnmarshalText decodes the address from hex. The 0x prefix is optional.
func (a *UnprefixedAddress) UnmarshalText(input []byte) error {
	return hexutil.UnmarshalFixedUnprefixedText("UnprefixedAddress", input, a[:])
}

// MarshalText encodes the address as hex.
func (a UnprefixedAddress) MarshalText() ([]byte, error) {
	return []byte(hex.EncodeToString(a[:])), nil
}

// MixedcaseAddress retains the original string, which may or may not be
// correctly checksummed
type MixedcaseAddress struct {
	addr     Address
	original string
}

// NewMixedcaseAddress constructor (mainly for testing)
func NewMixedcaseAddress(addr Address) MixedcaseAddress {
	return MixedcaseAddress{addr: addr, original: addr.Hex()}
}

// NewMixedcaseAddressFromString is mainly meant for unit-testing
func NewMixedcaseAddressFromString(hexaddr string) (*MixedcaseAddress, error) {
	if !IsHexAddress(hexaddr) {
		return nil, errors.New("invalid address")
	}
	a := FromHex(hexaddr)
	return &MixedcaseAddress{addr: BytesToAddress(a), original: hexaddr}, nil
}

// UnmarshalJSON parses MixedcaseAddress
func (ma *MixedcaseAddress) UnmarshalJSON(input []byte) error {
	if err := hexutil.UnmarshalFixedJSON(addressT, input, ma.addr[:]); err != nil {
		return err
	}
	return json.Unmarshal(input, &ma.original)
}

// MarshalJSON marshals the original value
func (ma *MixedcaseAddress) MarshalJSON() ([]byte, error) {
	if strings.HasPrefix(ma.original, "0x") || strings.HasPrefix(ma.original, "0X") {
		return json.Marshal(fmt.Sprintf("0x%s", ma.original[2:]))
	}
	return json.Marshal(fmt.Sprintf("0x%s", ma.original))
}

// Address returns the address
func (ma *MixedcaseAddress) Address() Address {
	return ma.addr
}

// String implements fmt.Stringer
func (ma *MixedcaseAddress) String() string {
	if ma.ValidChecksum() {
		return fmt.Sprintf("%s [chksum ok]", ma.original)
	}
	return fmt.Sprintf("%s [chksum INVALID]", ma.original)
}

// ValidChecksum returns true if the address has valid checksum
func (ma *MixedcaseAddress) ValidChecksum() bool {
	return ma.original == ma.addr.Hex()
}

// Original returns the mixed-case input string
func (ma *MixedcaseAddress) Original() string {
	return ma.original
}

// Location of a chain within the Quai hierarchy
// Location is encoded as a path from the root of the tree to the specified
// chain. Not all indices need to be populated, e.g:
// prime     = []
// region[0] = [0]
// zone[1,2] = [1, 2]
type Location []byte

func (loc Location) Region() int {
	if len(loc) >= 1 {
		return int(loc[REGION_CTX-1])
	} else {
		return -1
	}
}

func (loc Location) HasRegion() bool {
	return loc.Region() >= 0
}

func (loc Location) Zone() int {
	if len(loc) >= 2 {
		return int(loc[ZONE_CTX-1])
	} else {
		return -1
	}
}

func (loc Location) HasZone() bool {
	return loc.Zone() >= 0
}

func (loc Location) AssertValid() {
	if !loc.HasRegion() && loc.HasZone() {
		log.Fatal("cannot specify zone without also specifying region.")
	}
	if loc.Region() >= NumRegionsInPrime {
		log.Fatal("region index is not valid.")
	}
	if loc.Zone() >= NumZonesInRegion {
		log.Fatal("zone index is not valid.")
	}
}

func (loc Location) Context() int {
	loc.AssertValid()
	if loc.Zone() >= 0 {
		return ZONE_CTX
	} else if loc.Region() >= 0 {
		return REGION_CTX
	} else {
		return PRIME_CTX
	}
}

// DomLocation returns the location of your dominant chain
func (loc Location) DomLocation() Location {
	if len(loc) < 1 {
		return nil
	} else {
		return loc[:len(loc)-1]
	}
}

// SubIndex returns the index of the subordinate chain for a given location
func (loc Location) SubIndex() int {
	switch NodeLocation.Context() {
	case PRIME_CTX:
		return loc.Region()
	case REGION_CTX:
		return loc.Zone()
	default:
		return -1
	}
}

// SubInSlice returns the location of the subordinate chain within the specified
// slice. For example:
// * if prime calls SubInSlice(Location{0,0}) the result will be Location{0},
//   i.e. region-0's location, because Prime's subordinate in that slice is
//   region-0
// * if region-0 calls SubInSlice(Location{0,0}) the result will be
//   Location{0,0}, i.e. zone-0-0's location, because region-0's subordinate in
//   that slice is zone-0-0
func (loc Location) SubInSlice(slice Location) Location {
	if len(slice) <= len(loc) {
		log.Println("cannot determine sub location, because slice location is not deeper than self")
		return nil
	}
	subLoc := append(loc, slice[len(loc)])
	return subLoc
}

func (loc Location) InSameSliceAs(cmp Location) bool {
	// Figure out which location is shorter
	shorter := loc
	longer := cmp
	if len(loc) > len(cmp) {
		longer = loc
		shorter = cmp
	}
	// Compare bytes up to the shorter depth
	return shorter.Equal(longer[:len(shorter)])
}

func (loc Location) Name() string {
	regionName := ""
	switch loc.Region() {
	case 0:
		regionName = "cyprus"
	case 1:
		regionName = "paxos"
	case 2:
		regionName = "hydra"
	default:
		regionName = "unknownregion"
	}
	zoneNum := strconv.Itoa(loc.Zone()+1)
	switch loc.Context() {
	case PRIME_CTX:
		return "prime"
	case REGION_CTX:
		return regionName
	case ZONE_CTX:
		return regionName + zoneNum
	default:
		log.Println("cannot name invalid location")
		return "invalid-location"
	}
}

func (loc Location) Equal(cmp Location) bool {
	return bytes.Equal(loc, cmp)
}

// CommonDom identifies the highest context chain which exists in both locations
// * zone-0-0 & zone-0-1 would share region-0 as their highest context common dom
// * zone-0-0 & zone-1-0 would share Prime as their highest context common dom
func (loc Location) CommonDom(cmp Location) Location {
	common := Location{}
	shorterLen := len(loc)
	if len(loc) > len(cmp) {
		shorterLen = len(cmp)
	}
	for i := 0; i < shorterLen; i++ {
		if loc[i] == cmp[i] {
			common = append(common, loc[i])
		} else {
			break
		}
	}
	return common
}

func (l Location) ContainsAddress(a Address) bool {
	prefix := a[0]
	prefixRange, ok := locationToPrefixRange[l.Name()]
	if !ok {
		log.Fatal("unable to get address prefix range for location")
	}
	// Ranges are fully inclusive
	return uint8(prefix) >= prefixRange.lo && uint8(prefix) <= prefixRange.hi
}

func (l Location) RPCMarshal() []hexutil.Uint64 {
	res := make([]hexutil.Uint64, 0)
	for _, i := range l {
		res = append(res, hexutil.Uint64(i))
	}

	return res
}
