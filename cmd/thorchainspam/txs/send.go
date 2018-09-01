package txs

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"

	"github.com/cosmos/cosmos-sdk/client/context"
	"github.com/cosmos/cosmos-sdk/client/keys"
	"github.com/cosmos/cosmos-sdk/wire"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/thorchain/THORChain/cmd/thorchainspam/constants"

	cryptokeys "github.com/cosmos/cosmos-sdk/crypto/keys"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authcmd "github.com/cosmos/cosmos-sdk/x/auth/client/cli"
	"github.com/cosmos/cosmos-sdk/x/bank/client"
)

// Returns the command to ensure k accounts exist
func GetTxsSend(cdc *wire.Codec) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := context.NewCoreContextFromViper().WithDecoder(authcmd.GetAccountDecoder(cdc))

		// get list of all accounts starting with "spam"
		spamAccs, err := getSpamAccs()
		if err != nil {
			return err
		}

		// ensure spamAccs are present
		if len(spamAccs) == 0 {
			return fmt.Errorf("no spam accounts found, please create them with `thorchainspam account ensure`")
		}

		fmt.Printf("Found %v spam accounts\n", len(spamAccs))

		sem := make(chan struct{}, 100)
		var wg sync.WaitGroup

		for i := 0; i < len(spamAccs); i++ {
			wg.Add(1)

			// acquire semaphore
			sem <- struct{}{}

			go func(i int) {
				defer wg.Done()

				sendTxToRandomAcc(ctx, i, spamAccs, cdc)

				// release semaphore
				<-sem
			}(i)
		}

		wg.Wait()

		fmt.Printf("Done.")

		return nil
	}
}

func getSpamAccs() ([]cryptokeys.Info, error) {
	kb, err := keys.GetKeyBase()
	if err != nil {
		return nil, err
	}

	infos, err := kb.List()
	if err != nil {
		return nil, err
	}

	res := make([]cryptokeys.Info, 0, len(infos))

	for _, info := range infos {
		if strings.HasPrefix(info.GetName(), constants.SpamAccountPrefix) {
			res = append(res, info)
		}
	}

	return res, nil
}

func sendTxToRandomAcc(ctx context.CoreContext, i int, spamAccs []cryptokeys.Info, cdc *wire.Codec) {
	from := spamAccs[i]

	fmt.Printf("Iteration %v: Will send from account %v\n", i, from.GetName())

	// get account balance from sender
	fromAcc, err := getAcc(ctx, from)
	if err != nil {
		fmt.Printf("Iteration %v: Account not found, skipping\n", i)
		return
	}

	// calculate random share of coins to be sent
	coins := getRandomCoinsUpTo(fromAcc.GetCoins(), 0.5)

	if !coins.IsPositive() {
		fmt.Printf("Iteration %v: No coins to send, skipping\n", i)
		return
	}

	// get random account to send to
	to := spamAccs[rand.Intn(len(spamAccs))]

	fmt.Printf("Iteration %v: Will send %v from %v to %v\n", i, coins.String(), from.GetName(), to.GetName())

	// build and sign the transaction, then broadcast to Tendermint
	msg := client.BuildMsg(fromAcc.GetAddress(), getAddr(to), coins)
	err = ensureSignBuildBroadcast(ctx, fromAcc, from.GetName(), constants.SpamAccountPassword, []sdk.Msg{msg}, cdc)
	if err != nil {
		fmt.Println(err)
	}
}

func getRandomCoinsUpTo(coins sdk.Coins, max float32) sdk.Coins {
	res := make([]sdk.Coin, 0, len(coins))

	mult := rand.Int63n(int64(9999*max)) + 1

	for _, coin := range coins {
		res = append(res, sdk.Coin{
			Denom:  coin.Denom,
			Amount: coin.Amount.MulRaw(mult).DivRaw(int64(10000)),
		})
	}
	return res
}

func getAddr(info cryptokeys.Info) []byte {
	return sdk.AccAddress(info.GetPubKey().Address())
}

func getAcc(ctx context.CoreContext, info cryptokeys.Info) (auth.Account, error) {
	accAddr := getAddr(info)

	accBytes, err := ctx.QueryStore(auth.AddressStoreKey(accAddr), ctx.AccountStore)
	if err != nil {
		return nil, err
	}

	// Check if account was found
	if accBytes == nil {
		return nil, errors.Errorf("No account with address %s was found in the state.\nAre you sure there has been a transaction involving it?", accAddr)
	}

	// Decode account
	acc, err := ctx.Decoder(accBytes)
	if err != nil {
		return nil, err
	}

	return acc, nil
}

// sign and build the transaction from the msg
func ensureSignBuildBroadcast(ctx context.CoreContext, account auth.Account, name string, passphrase string, msgs []sdk.Msg, cdc *wire.Codec) (err error) {
	ctx = ctx.WithAccountNumber(account.GetAccountNumber())
	ctx = ctx.WithSequence(account.GetSequence())
	ctx = ctx.WithGas(int64(10000))

	var txBytes []byte

	txBytes, err = ctx.SignAndBuild(name, passphrase, msgs, cdc)
	if err != nil {
		return fmt.Errorf("Error signing transaction: %v", err)
	}

	if ctx.Async {
		res, err := ctx.BroadcastTxAsync(txBytes)
		if err != nil {
			return err
		}
		if ctx.JSON {
			type toJSON struct {
				TxHash string
			}
			valueToJSON := toJSON{res.Hash.String()}
			JSON, err := cdc.MarshalJSON(valueToJSON)
			if err != nil {
				return err
			}
			fmt.Println(string(JSON))
		} else {
			fmt.Println("Async tx sent. tx hash: ", res.Hash.String())
		}
		return nil
	}
	res, err := ctx.BroadcastTx(txBytes)
	if err != nil {
		return err
	}
	if ctx.JSON {
		// Since JSON is intended for automated scripts, always include response in JSON mode
		type toJSON struct {
			Height   int64
			TxHash   string
			Response string
		}
		valueToJSON := toJSON{res.Height, res.Hash.String(), fmt.Sprintf("%+v", res.DeliverTx)}
		JSON, err := cdc.MarshalJSON(valueToJSON)
		if err != nil {
			return err
		}
		fmt.Println(string(JSON))
		return nil
	}
	if ctx.PrintResponse {
		fmt.Printf("Committed at block %d. Hash: %s Response:%+v \n", res.Height, res.Hash.String(), res.DeliverTx)
	} else {
		fmt.Printf("Committed at block %d. Hash: %s \n", res.Height, res.Hash.String())
	}
	return nil
}