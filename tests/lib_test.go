package tests

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/idena-network/idena-wasm-binding/lib"
	"github.com/idena-network/idena-wasm-binding/tests/testdata"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
	"golang.org/x/crypto/sha3"
)

type contractValue struct {
	value   []byte
	removed bool
}

type ContractData struct {
	Code []byte
}

type ContractContext struct {
	caller       lib.Address
	originCaller lib.Address
	contractAddr lib.Address
	payAmount    *big.Int
}

func (ctx *ContractContext) ContractAddr() lib.Address {
	return ctx.contractAddr
}

func (ctx *ContractContext) CreateSubContext(contract lib.Address, payAmount *big.Int) *ContractContext {
	return &ContractContext{
		caller:       ctx.ContractAddr(),
		originCaller: ctx.originCaller,
		contractAddr: contract,
		payAmount:    cloneBig(payAmount),
	}
}

type MockDb struct {
	db *db.MemDB
}

func (db *MockDb) GetContractValue(contract lib.Address, key []byte) []byte {
	formattedKey := append(append([]byte{0x5}, contract[:]...), key...)
	v, _ := db.db.Get(formattedKey)
	return cloneBytes(v)
}

type mockEvent struct {
	contract lib.Address
	name     string
	args     [][]byte
}

type MockHostEnv struct {
	parent *MockHostEnv
	ctx    *ContractContext
	db     *MockDb

	contractStoreCache    map[lib.Address]map[string]*contractValue
	balancesCache         map[lib.Address]*big.Int
	deployedContractCache map[lib.Address]ContractData
	contractStakeCache    map[lib.Address]*big.Int
	identities            map[lib.Address][]byte
	blockHeaders          map[uint64][]byte
	globalState           []byte
	events                []mockEvent
}

func (e *MockHostEnv) Burn(meter *lib.GasMeter, amount *big.Int) error {
	return e.SubBalance(meter, amount)
}

func (e *MockHostEnv) Ecrecover(meter *lib.GasMeter, data []byte, signature []byte) []byte {
	consumeGas(meter, 300)
	return nil
}

func (e *MockHostEnv) GlobalState(meter *lib.GasMeter) []byte {
	consumeGas(meter, 10)
	return e.getGlobalState()
}

func (e *MockHostEnv) BlockHeader(meter *lib.GasMeter, height uint64) []byte {
	consumeGas(meter, 10)
	return e.getBlockHeader(height)
}

func (e *MockHostEnv) Keccak256(meter *lib.GasMeter, data []byte) []byte {
	consumeGas(meter, 10)
	return keccakHash(data)
}

func (e *MockHostEnv) IsDebug() bool {
	return true
}

func NewMockHostEnv() *MockHostEnv {
	env := &MockHostEnv{
		db: &MockDb{
			db: db.NewMemDB(),
		},
		ctx: &ContractContext{
			contractAddr: lib.Address{0x1},
			caller:       lib.Address{0x2},
			originCaller: lib.Address{0x3},
			payAmount:    big.NewInt(10),
		},
		deployedContractCache: map[lib.Address]ContractData{},
		contractStakeCache:    map[lib.Address]*big.Int{},
		balancesCache:         map[lib.Address]*big.Int{},
		contractStoreCache:    map[lib.Address]map[string]*contractValue{},
		identities:            map[lib.Address][]byte{},
		blockHeaders:          map[uint64][]byte{},
	}
	env.setBalance(env.ctx.ContractAddr(), big.NewInt(1_000_000))
	return env
}

func newChildMockHostEnv(parent *MockHostEnv, contract lib.Address, payAmount *big.Int) *MockHostEnv {
	child := &MockHostEnv{
		parent:                parent,
		ctx:                   parent.ctx.CreateSubContext(contract, payAmount),
		db:                    parent.db,
		deployedContractCache: map[lib.Address]ContractData{},
		contractStakeCache:    map[lib.Address]*big.Int{},
		balancesCache:         map[lib.Address]*big.Int{},
		contractStoreCache:    map[lib.Address]map[string]*contractValue{},
		identities:            map[lib.Address][]byte{},
		blockHeaders:          map[uint64][]byte{},
		globalState:           cloneBytes(parent.globalState),
	}
	child.setBalance(contract, new(big.Int).Add(parent.getBalance(contract), cloneBig(payAmount)))
	return child
}

func consumeGas(meter *lib.GasMeter, amount uint64) {
	if meter != nil {
		meter.ConsumeGas(amount)
	}
}

func cloneBytes(data []byte) []byte {
	if data == nil {
		return nil
	}
	return append([]byte(nil), data...)
}

func cloneBig(value *big.Int) *big.Int {
	if value == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(value)
}

func cloneArgs(args [][]byte) [][]byte {
	if args == nil {
		return nil
	}
	cloned := make([][]byte, len(args))
	for i := range args {
		cloned[i] = cloneBytes(args[i])
	}
	return cloned
}

func keccakHash(parts ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	return h.Sum(nil)
}

func addressFromBytes(data []byte) lib.Address {
	if len(data) > len(lib.Address{}) {
		data = data[len(data)-len(lib.Address{}):]
	}
	var address lib.Address
	copy(address[len(address)-len(data):], data)
	return address
}

func computeContractAddress(code []byte, args []byte, nonce []byte) lib.Address {
	return computeContractAddressByHash(keccakHash(code), args, nonce)
}

func computeContractAddressByHash(codeHash []byte, args []byte, nonce []byte) lib.Address {
	data := make([]byte, 0, len(codeHash)+len(args)+len(nonce))
	data = append(data, codeHash...)
	data = append(data, args...)
	data = append(data, nonce...)
	return addressFromBytes(keccakHash(data))
}

func (e *MockHostEnv) SetStorage(meter *lib.GasMeter, key []byte, value []byte) {
	ctx := e.ctx
	if len(key) > 32 {
		panic("key is too big")
	}
	addr := ctx.ContractAddr()
	var cache map[string]*contractValue
	var ok bool
	if cache, ok = e.contractStoreCache[addr]; !ok {
		cache = make(map[string]*contractValue)
		e.contractStoreCache[addr] = cache
	}
	cache[string(key)] = &contractValue{
		value:   cloneBytes(value),
		removed: false,
	}
	consumeGas(meter, uint64(10*(len(key)+len(value))))
}

func (e *MockHostEnv) GetStorage(meter *lib.GasMeter, key []byte) []byte {
	value := e.readContractData(e.ctx.ContractAddr(), key)
	consumeGas(meter, uint64(10*len(value)))
	return value
}

func (e *MockHostEnv) RemoveStorage(meter *lib.GasMeter, key []byte) {
	addr := e.ctx.ContractAddr()
	var cache map[string]*contractValue
	var ok bool
	if cache, ok = e.contractStoreCache[addr]; !ok {
		cache = map[string]*contractValue{}
		e.contractStoreCache[addr] = cache
	}
	cache[string(key)] = &contractValue{removed: true}
	consumeGas(meter, 10)
}

func (e *MockHostEnv) readContractData(contractAddr lib.Address, key []byte) []byte {
	if cache, ok := e.contractStoreCache[contractAddr]; ok {
		if value, ok := cache[string(key)]; ok {
			if value.removed {
				return nil
			}
			return cloneBytes(value.value)
		}
	}

	if e.parent != nil {
		return e.parent.readContractData(contractAddr, key)
	}

	value := e.db.GetContractValue(contractAddr, key)

	return value
}

func (e *MockHostEnv) BlockNumber(meter *lib.GasMeter) uint64 {
	consumeGas(meter, 10)
	return 1
}

func (e *MockHostEnv) BlockTimestamp(meter *lib.GasMeter) int64 {
	consumeGas(meter, 10)
	return 1_700_000_000
}

func (e *MockHostEnv) MinFeePerGas(meter *lib.GasMeter) *big.Int {
	consumeGas(meter, 10)
	return big.NewInt(1)
}

func (e *MockHostEnv) Balance(meter *lib.GasMeter) *big.Int {
	consumeGas(meter, 10)
	return e.getBalance(e.ctx.ContractAddr())
}

func (e *MockHostEnv) BlockSeed(meter *lib.GasMeter) []byte {
	consumeGas(meter, 10)
	return keccakHash([]byte("mock-block-seed"))
}

func (e *MockHostEnv) NetworkSize(meter *lib.GasMeter) uint64 {
	consumeGas(meter, 10)
	return 1
}

func (e *MockHostEnv) IdentityState(meter *lib.GasMeter, address lib.Address) byte {
	consumeGas(meter, 10)
	if identity := e.Identity(meter, address); len(identity) > 0 {
		return identity[0]
	}
	return 0
}

func (e *MockHostEnv) Identity(meter *lib.GasMeter, address lib.Address) []byte {
	consumeGas(meter, 10)
	if identity, ok := e.identities[address]; ok {
		return cloneBytes(identity)
	}
	if e.parent != nil {
		return e.parent.Identity(meter, address)
	}
	return nil
}

func (e *MockHostEnv) CreateSubEnv(contract lib.Address, method string, payAmount *big.Int, isDeploy bool) (lib.HostEnv, error) {
	if payAmount != nil && payAmount.Sign() < 0 {
		return nil, errors.New("value must be non-negative")
	}
	return newChildMockHostEnv(e, contract, payAmount), nil
}

func (e *MockHostEnv) GetCode(addr lib.Address) []byte {
	if data, ok := e.contractData(addr); ok {
		return cloneBytes(data.Code)
	}
	return nil
}

func (e *MockHostEnv) Commit() {
	if e.parent == nil {
		return
	}
	for contract, cache := range e.contractStoreCache {
		if e.parent.contractStoreCache[contract] == nil {
			e.parent.contractStoreCache[contract] = map[string]*contractValue{}
		}
		for key, value := range cache {
			e.parent.contractStoreCache[contract][key] = &contractValue{
				value:   cloneBytes(value.value),
				removed: value.removed,
			}
		}
	}
	for address, balance := range e.balancesCache {
		e.parent.balancesCache[address] = cloneBig(balance)
	}
	for contract, data := range e.deployedContractCache {
		e.parent.deployedContractCache[contract] = ContractData{Code: cloneBytes(data.Code)}
	}
	for contract, stake := range e.contractStakeCache {
		e.parent.contractStakeCache[contract] = cloneBig(stake)
	}
	for _, event := range e.events {
		e.parent.events = append(e.parent.events, mockEvent{
			contract: event.contract,
			name:     event.name,
			args:     cloneArgs(event.args),
		})
	}
}

func (e *MockHostEnv) Caller(meter *lib.GasMeter) lib.Address {
	consumeGas(meter, 10)
	return e.ctx.caller
}

func (e *MockHostEnv) OriginalCaller(meter *lib.GasMeter) lib.Address {
	consumeGas(meter, 10)
	return e.ctx.originCaller
}

func (e *MockHostEnv) SubBalance(meter *lib.GasMeter, amount *big.Int) error {
	consumeGas(meter, 10)
	amount = cloneBig(amount)
	if amount.Sign() < 0 {
		return errors.New("value must be non-negative")
	}
	balance := e.getBalance(e.ctx.ContractAddr())
	if balance.Cmp(amount) < 0 {
		return errors.New("insufficient funds")
	}
	e.setBalance(e.ctx.ContractAddr(), new(big.Int).Sub(balance, amount))
	return nil
}

func (e *MockHostEnv) AddBalance(meter *lib.GasMeter, address lib.Address, amount *big.Int) {
	consumeGas(meter, 10)
	e.setBalance(address, new(big.Int).Add(e.getBalance(address), cloneBig(amount)))
}

func (e *MockHostEnv) ContractAddress(meter *lib.GasMeter) lib.Address {
	consumeGas(meter, 10)
	return e.ctx.ContractAddr()
}

func (e *MockHostEnv) ContractAddr(meter *lib.GasMeter, code []byte, args []byte, nonce []byte) lib.Address {
	consumeGas(meter, 20)
	return computeContractAddress(code, args, nonce)
}

func (e *MockHostEnv) Deploy(code []byte) {
	e.deployedContractCache[e.ctx.ContractAddr()] = ContractData{Code: cloneBytes(code)}
}

func (e *MockHostEnv) ContractAddrByHash(meter *lib.GasMeter, hash []byte, args []byte, nonce []byte) lib.Address {
	consumeGas(meter, 10)
	return computeContractAddressByHash(hash, args, nonce)
}

func (e *MockHostEnv) OwnCode(meter *lib.GasMeter) []byte {
	consumeGas(meter, 10)
	return e.GetCode(e.ctx.ContractAddr())
}

func (e *MockHostEnv) CodeHash(meter *lib.GasMeter) []byte {
	consumeGas(meter, 10)
	return keccakHash(e.OwnCode(meter))
}

func (e *MockHostEnv) Event(meter *lib.GasMeter, name string, args ...[]byte) {
	consumeGas(meter, uint64(10+len(name)))
	e.events = append(e.events, mockEvent{
		contract: e.ctx.ContractAddr(),
		name:     name,
		args:     cloneArgs(args),
	})
}

func (e *MockHostEnv) ReadContractData(meter *lib.GasMeter, address lib.Address, key []byte) []byte {
	value := e.readContractData(address, key)
	consumeGas(meter, uint64(10*len(value)))
	return value
}

func (e *MockHostEnv) Epoch(meter *lib.GasMeter) uint16 {
	consumeGas(meter, 10)
	return 1
}

func (e *MockHostEnv) ContractCodeHash(addr lib.Address) *[]byte {
	if data, ok := e.contractData(addr); ok {
		hash := keccakHash(data.Code)
		return &hash
	}
	return nil
}

func (e *MockHostEnv) PayAmount(meter *lib.GasMeter) *big.Int {
	consumeGas(meter, 10)
	return cloneBig(e.ctx.payAmount)
}

func (e *MockHostEnv) contractData(addr lib.Address) (ContractData, bool) {
	if data, ok := e.deployedContractCache[addr]; ok {
		return ContractData{Code: cloneBytes(data.Code)}, true
	}
	if e.parent != nil {
		return e.parent.contractData(addr)
	}
	return ContractData{}, false
}

func (e *MockHostEnv) getBlockHeader(height uint64) []byte {
	if header, ok := e.blockHeaders[height]; ok {
		return cloneBytes(header)
	}
	if e.parent != nil {
		return e.parent.getBlockHeader(height)
	}
	return []byte{}
}

func (e *MockHostEnv) getGlobalState() []byte {
	if e.globalState != nil {
		return cloneBytes(e.globalState)
	}
	if e.parent != nil {
		return e.parent.getGlobalState()
	}
	return nil
}

func (e *MockHostEnv) getBalance(address lib.Address) *big.Int {
	if balance, ok := e.balancesCache[address]; ok {
		return cloneBig(balance)
	}
	if e.parent != nil {
		return e.parent.getBalance(address)
	}
	return new(big.Int)
}

func (e *MockHostEnv) setBalance(address lib.Address, amount *big.Int) {
	e.balancesCache[address] = cloneBig(amount)
}

func ToBytes(value interface{}) []byte {
	buf := new(bytes.Buffer)
	err := binary.Write(buf, binary.LittleEndian, value)
	if err != nil {
		fmt.Println("binary.Write failed:", err)
	}
	return buf.Bytes()
}

func TestMockHostEnvDeterministicReads(t *testing.T) {
	env := NewMockHostEnv()
	meter := &lib.GasMeter{}

	expectedHash, err := hex.DecodeString("4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45")
	require.NoError(t, err)
	require.Equal(t, expectedHash, env.Keccak256(meter, []byte("abc")))
	require.Equal(t, uint64(1), env.BlockNumber(meter))
	require.Equal(t, int64(1_700_000_000), env.BlockTimestamp(meter))
	require.Equal(t, big.NewInt(1), env.MinFeePerGas(meter))
	require.Len(t, env.BlockSeed(meter), 32)
	require.Equal(t, uint64(1), env.NetworkSize(meter))
	require.Equal(t, uint16(1), env.Epoch(meter))

	env.globalState = []byte("global")
	env.blockHeaders[7] = []byte("header")
	identityAddr := lib.Address{0x7}
	env.identities[identityAddr] = []byte{0x5, 0x6}

	require.Equal(t, []byte("global"), env.GlobalState(meter))
	require.Equal(t, []byte("header"), env.BlockHeader(meter, 7))
	require.Equal(t, []byte{}, env.BlockHeader(meter, 8))
	require.Equal(t, []byte{0x5, 0x6}, env.Identity(meter, identityAddr))
	require.Equal(t, byte(0x5), env.IdentityState(meter, identityAddr))
	require.Nil(t, env.Ecrecover(meter, []byte("data"), []byte("bad signature")))
}

func TestMockHostEnvContractCodeAndAddress(t *testing.T) {
	env := NewMockHostEnv()
	meter := &lib.GasMeter{}
	code := []byte("wasm-code")
	args := lib.PackArguments([][]byte{[]byte("arg")})
	nonce := []byte{0x1, 0x2}

	addr := env.ContractAddr(meter, code, args, nonce)
	require.Equal(t, env.ContractAddrByHash(meter, env.Keccak256(meter, code), args, nonce), addr)

	env.ctx.contractAddr = addr
	env.Deploy(code)

	require.Equal(t, code, env.GetCode(addr))
	require.Equal(t, code, env.OwnCode(meter))
	require.Equal(t, env.Keccak256(meter, code), env.CodeHash(meter))
	codeHash := env.ContractCodeHash(addr)
	require.NotNil(t, codeHash)
	require.Equal(t, env.Keccak256(meter, code), *codeHash)
	require.Nil(t, env.ContractCodeHash(lib.Address{0xff}))
}

func TestMockHostEnvBalances(t *testing.T) {
	env := NewMockHostEnv()
	meter := &lib.GasMeter{}

	env.setBalance(env.ctx.ContractAddr(), big.NewInt(5))
	require.Equal(t, big.NewInt(5), env.Balance(meter))
	require.NoError(t, env.SubBalance(meter, big.NewInt(3)))
	require.Equal(t, big.NewInt(2), env.Balance(meter))
	require.EqualError(t, env.SubBalance(meter, big.NewInt(3)), "insufficient funds")
	require.EqualError(t, env.Burn(meter, big.NewInt(-1)), "value must be non-negative")

	recipient := lib.Address{0x9}
	env.AddBalance(meter, recipient, big.NewInt(4))
	require.Equal(t, big.NewInt(4), env.getBalance(recipient))
}

func TestMockHostEnvSubEnvCommit(t *testing.T) {
	parent := NewMockHostEnv()
	meter := &lib.GasMeter{}
	contract := lib.Address{0x9}
	parent.globalState = []byte("root-state")
	parent.blockHeaders[3] = []byte("root-header")

	subHost, err := parent.CreateSubEnv(contract, "call", big.NewInt(7), false)
	require.NoError(t, err)
	child := subHost.(*MockHostEnv)

	require.Equal(t, parent.ctx.ContractAddr(), child.Caller(meter))
	require.Equal(t, parent.ctx.originCaller, child.OriginalCaller(meter))
	require.Equal(t, contract, child.ContractAddress(meter))
	require.Equal(t, big.NewInt(7), child.PayAmount(meter))
	require.Equal(t, big.NewInt(7), child.Balance(meter))
	require.Equal(t, []byte("root-state"), child.GlobalState(meter))
	require.Equal(t, []byte("root-header"), child.BlockHeader(meter, 3))

	child.SetStorage(meter, []byte("key"), []byte("value"))
	child.AddBalance(meter, contract, big.NewInt(5))
	child.Deploy([]byte("child-code"))
	child.Event(meter, "Done", []byte("arg"))
	child.Commit()

	require.Equal(t, []byte("value"), parent.ReadContractData(meter, contract, []byte("key")))
	require.Equal(t, big.NewInt(12), parent.getBalance(contract))
	require.Equal(t, []byte("child-code"), parent.GetCode(contract))
	require.Len(t, parent.events, 1)
	require.Equal(t, contract, parent.events[0].contract)
	require.Equal(t, "Done", parent.events[0].name)
	require.Equal(t, [][]byte{[]byte("arg")}, parent.events[0].args)
}

func TestSum(t *testing.T) {
	code, _ := testdata.Sum()

	api := lib.NewGoAPI(NewMockHostEnv(), &lib.GasMeter{})

	_, _, err := lib.Deploy(api, code, [][]byte{ToBytes(uint64(1))}, lib.Address{}, 10000000, true)
	require.NoError(t, err)
	_, _, err = lib.Execute(api, code, "compute", [][]byte{ToBytes(uint64(10))}, lib.Address{}, 1000000, true)
	require.NoError(t, err)
}
