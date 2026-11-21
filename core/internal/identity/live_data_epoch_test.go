package identity

import (
	"context"
	"testing"
)

func TestRevokeAccountEverywhereAdvancesLiveDataEpoch(t *testing.T) {
	svc, st := agentTokenSvc(t)
	ctx := context.Background()
	var account string
	if err := st.Pool().QueryRow(ctx, `INSERT INTO accounts(provider,provider_subject,email,role) VALUES('local','live-epoch','live-epoch@example.com','member') RETURNING id::text`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeAccountEverywhere(ctx, account); err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := st.Pool().QueryRow(ctx, `SELECT live_data_epoch FROM accounts WHERE id=$1`, account).Scan(&epoch); err != nil || epoch != 2 {
		t.Fatalf("live-data epoch=%d err=%v", epoch, err)
	}
}
