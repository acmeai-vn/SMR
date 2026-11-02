# SMR testing channel

Use this path before production. SMR has no separate testing binary; the
environment is selected by the classifier URL and the ACME AI API key.

| Item | Testing value |
|---|---|
| Web dashboard | `https://llm-testing.acmeai.vn` |
| Classification API | `https://api-testing.acmeai.vn` |
| API key | Created on the testing dashboard |
| Database and SePay | Testing database and SePay testing account only |

Never reuse a production `acme_sk_...` key on the testing API, and never point
the testing build at `api.acmeai.vn`.

## 1. Prepare the testing account

1. Open `https://llm-testing.acmeai.vn` and sign in with Google or GitHub.
2. Top up 20,000 VND using the testing TP Bank QR.
3. Create an API key and copy it once.
4. Confirm `https://api-testing.acmeai.vn/health` and `/ready` return 200.

The API key is shown only once. SMR stores it locally and never sends it to the
model provider.

## 2. Point SMR at testing

Edit the SMR config:

    classifier:
      base_url: "https://api-testing.acmeai.vn"
      api_key: "acme_sk_testing_key"

Or keep the key out of the file and use environment variables:

    ACMEAI_BASE_URL=https://api-testing.acmeai.vn
    ACMEAI_API_KEY=acme_sk_testing_key

`config.testing.example.yaml` contains a full starting point with all four tiers
for both profiles. Restart SMR after changing the file if the service does not
reload it:

    smr restart

## 3. Verify

    smr doctor
    smr status

`smr doctor` must report the testing balance and four configured tiers. Send a
request through Codex or Claude Code, then check:

    X-SMR-Tier
    X-SMR-Cache
    X-SMR-Charged-VND
    X-SMR-Balance-VND
    X-SMR-Classifier

The matching row must appear on the testing dashboard with the same charge.

## 4. Failure cases to exercise

- Empty balance: SMR returns 402 before calling a model.
- Revoked API key: SMR reports a classifier authentication failure and does not
  call the model.
- Repeated identical state within five minutes: `X-SMR-Cache: hit` and a charge
  equal to 10% of the original, minimum 1 VND.
- Same `Idempotency-Key` with the same body: no second charge.
- Classifier timeout: configured fallback tier is used once, and the request is
  not double-charged.

## 5. Exit criteria

- `smr doctor` passes against `api-testing.acmeai.vn`.
- Codex and Claude Code both route through SMR against the testing API.
- Dashboard totals, ledger entries, and `X-SMR-Charged-VND` agree.
- No raw prompt, API key, or provider credential appears in `smr logs`.

