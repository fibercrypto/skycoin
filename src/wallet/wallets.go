package wallet

import (
    "github.com/skycoin/skycoin/src/coin"
    "io/ioutil"
    "path/filepath"
    "strings"
)

type Wallets []Wallet

// Loads all wallets contained in wallet dir.  If any regular file in wallet
// dir fails to load, loading is aborted and error returned.  Only files with
// extension WalletExt are considered
func LoadWallets(dir string) (Wallets, error) {
    entries, err := ioutil.ReadDir(dir)
    if err != nil {
        return nil, err
    }
    wallets := make(Wallets, 0)
    for _, e := range entries {
        if e.Mode().IsRegular() {
            name := e.Name()
            if !strings.HasSuffix(name, WalletExt) {
                continue
            }
            rw, err := LoadReadableWallet(filepath.Join(dir, name))
            if err != nil {
                return nil, err
            }
            w, err := rw.ToWallet()
            if err != nil {
                return nil, err
            }
            w.SetFilename(name)
            wallets = append(wallets, w)
        }
    }
    return wallets, nil
}

func (self *Wallets) Add(w Wallet) {
    *self = append(*self, w)
}

func (self Wallets) Get(walletID WalletID) Wallet {
    for _, w := range self {
        if w.GetID() == walletID {
            return w
        }
    }
    return nil
}

func (self Wallets) Save(dir string) map[WalletID]error {
    errs := make(map[WalletID]error)
    for _, w := range self {
        if err := w.Save(dir); err != nil {
            errs[w.GetID()] = err
        }
    }
    return errs
}

func (self Wallets) GetAddressSet() map[coin.Address]byte {
    set := make(AddressSet)
    for _, w := range self {
        set.Update(w.GetAddressSet())
    }
    return set
}

func (self Wallets) ToReadable() []*ReadableWallet {
    rw := make([]*ReadableWallet, len(self))
    for i, w := range self {
        rw[i] = NewReadableWallet(w)
    }
    return rw
}
