package order

import (
	"testing"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/pool/account"
	"github.com/lightninglabs/pool/auctioneerrpc"
	"github.com/lightninglabs/pool/terms"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/stretchr/testify/require"
)

// TestValidateOrderAccountIsolation tests that when we go to validate that an
// account has sufficient balance for an order, we only factor in the
// outstanding orders for that account, and not all total orders that are open.
func TestValidateOrderAccountIsolation(t *testing.T) {
	t.Parallel()

	orderStore := newMockStore()
	orderManager := NewManager(&ManagerConfig{
		Store: orderStore,
	})

	// We'll now create two accounts, one that's 1 BTC in size, while the
	// other is 2 BTC.
	accountA := account.Account{
		Value: btcutil.SatoshiPerBitcoin,
		TraderKey: &keychain.KeyDescriptor{
			PubKey: acctKeySmall,
		},
	}
	accountB := account.Account{
		Value: btcutil.SatoshiPerBitcoin * 2,
		TraderKey: &keychain.KeyDescriptor{
			PubKey: acctKeyBig,
		},
	}

	// We'll now create an order that will consume at least half (1100
	// units is 1.1 BTC) of account B (it's an ask order).
	var acctKeyB [33]byte
	copy(acctKeyB[:], acctKeyBig.SerializeCompressed())
	orderB := &Ask{
		Kit: newKitFromTemplate(Nonce{0x01}, &Kit{
			State:            StateSubmitted,
			UnitsUnfulfilled: 1100,
			FixedRate:        2_000,
			MaxBatchFeeRate:  1000,
			AcctKey:          acctKeyB,
			LeaseDuration:    144,
			MinUnitsMatch:    100,
		}),
	}

	testTerms := &terms.AuctioneerTerms{
		OrderExecBaseFee: 1,
		OrderExecFeeRate: 100,
		LeaseDurationBuckets: map[uint32]auctioneerrpc.DurationBucketState{
			144: auctioneerrpc.DurationBucketState_MARKET_OPEN,
		},
	}

	// Submitting this order for account B should pass validation.
	err := orderManager.validateOrder(orderB, &accountB, testTerms)
	if err != nil {
		t.Fatalf("order B validation failed: %v", err)
	}

	// Simulating order acceptance, we'll now add this order to the order
	// store.
	if err := orderStore.SubmitOrder(orderB); err != nil {
		t.Fatalf("unable to submit order: %v", err)
	}

	// Next, we'll create a ask that'll consume _most_ (but not all) of account
	// A's balance. This should pass validation as although account B can't
	// handle the order account A can.
	var acctKeyA [33]byte
	copy(acctKeyA[:], acctKeySmall.SerializeCompressed())
	orderA := &Ask{
		Kit: newKitFromTemplate(Nonce{0x01}, &Kit{
			State:            StateSubmitted,
			UnitsUnfulfilled: 800,
			FixedRate:        2_000,
			MaxBatchFeeRate:  1000,
			AcctKey:          acctKeyA,
			LeaseDuration:    144,
			MinUnitsMatch:    100,
		}),
	}

	err = orderManager.validateOrder(orderA, &accountA, testTerms)
	if err != nil {
		t.Fatalf("order A failed validation: %v", err)
	}
}

// TestValidateOrder makes sure that the order manager properly validates
// all conditions for submitting orders, especially the conditions for using the
// self channel balance.
func TestValidateOrder(t *testing.T) {
	t.Parallel()

	orderStore := newMockStore()
	orderManager := NewManager(&ManagerConfig{
		Store: orderStore,
	})

	// We'll now create an account with sufficient size.
	testAccount := &account.Account{
		Value: btcutil.SatoshiPerBitcoin,
		TraderKey: &keychain.KeyDescriptor{
			PubKey: acctKeySmall,
		},
	}

	testTerms := &terms.AuctioneerTerms{
		OrderExecBaseFee: 1,
		OrderExecFeeRate: 100,
		LeaseDurationBuckets: map[uint32]auctioneerrpc.DurationBucketState{
			144: auctioneerrpc.DurationBucketState_MARKET_OPEN,
		},
	}

	testCases := []struct {
		name        string
		expectedErr string
		order       Order
	}{{
		name:        "invalid lease duration",
		expectedErr: "invalid lease duration, must be one of map[144",
		order: &Ask{
			Kit: Kit{
				LeaseDuration: 123,
			},
		},
	}, {
		name:        "invalid max batch fee rate",
		expectedErr: "invalid max batch fee rate 123 sat/kw, must be",
		order: &Ask{
			Kit: Kit{
				LeaseDuration:   144,
				MaxBatchFeeRate: 123,
			},
		},
	}, {
		name:        "insufficient account balance",
		expectedErr: "insufficient account balance",
		order: &Ask{
			Kit: Kit{
				LeaseDuration:    144,
				MaxBatchFeeRate:  253,
				MinUnitsMatch:    1,
				Amt:              btcutil.SatoshiPerBitcoin,
				UnitsUnfulfilled: 1_000,
			},
		},
	}, {
		name: "invalid version for self chan balance",
		expectedErr: "cannot use self chan balance with old order " +
			"version",
		order: &Bid{
			Kit: Kit{
				LeaseDuration:    144,
				MaxBatchFeeRate:  253,
				MinUnitsMatch:    1,
				Amt:              100_000,
				UnitsUnfulfilled: 1,
			},
			SelfChanBalance: 1,
		},
	}, {
		name:        "invalid capacity for self chan balance",
		expectedErr: "channel capacity must be positive multiple of",
		order: &Bid{
			Kit: Kit{
				Version:          VersionSelfChanBalance,
				LeaseDuration:    144,
				MaxBatchFeeRate:  253,
				MinUnitsMatch:    1,
				Amt:              0,
				UnitsUnfulfilled: 0,
			},
			SelfChanBalance: 1,
		},
	}, {
		name: "invalid self chan balance",
		expectedErr: "self channel balance must be smaller than " +
			"or equal to capacity",
		order: &Bid{
			Kit: Kit{
				Version:          VersionSelfChanBalance,
				LeaseDuration:    144,
				MaxBatchFeeRate:  253,
				MinUnitsMatch:    1,
				Amt:              100_000,
				UnitsUnfulfilled: 1,
			},
			SelfChanBalance: 100_001,
		},
	}, {
		name: "min units match must equal amount",
		expectedErr: "to use self chan balance the min units match " +
			"must be equal to the order amount in units",
		order: &Bid{
			Kit: Kit{
				Version:          VersionSelfChanBalance,
				LeaseDuration:    144,
				MaxBatchFeeRate:  253,
				MinUnitsMatch:    1,
				Amt:              200_000,
				UnitsUnfulfilled: 2,
			},
			SelfChanBalance: 50_000,
		},
	}, {
		name:        "happy path",
		expectedErr: "",
		order: &Bid{
			Kit: Kit{
				Version:          VersionSelfChanBalance,
				LeaseDuration:    144,
				MaxBatchFeeRate:  253,
				MinUnitsMatch:    1,
				Amt:              100_000,
				UnitsUnfulfilled: 1,
				Units:            1,
			},
			SelfChanBalance: 10_000,
		},
	}}

	for _, tc := range testCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			err := orderManager.validateOrder(
				tc.order, testAccount, testTerms,
			)

			if tc.expectedErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErr)
			}
		})
	}
}
