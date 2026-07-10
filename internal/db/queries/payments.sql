-- name: CreatePayment :one
insert into payments (booking_id, amount, currency, method, status, provider, provider_tx_ref)
values ($1, $2, $3, 'mobile_money', 'pending', 'flutterwave', $4)
returning id, status, provider_tx_ref, created_at;

-- name: GetPaymentByTxRef :one
select id, booking_id, amount, currency, method, status, provider,
       provider_tx_ref, provider_transaction_id, paid_at, created_at
from payments
where provider_tx_ref = $1;

-- name: GetPaymentByBooking :one
select id, booking_id, amount, currency, method, status, provider,
       provider_tx_ref, provider_transaction_id, paid_at, created_at
from payments
where booking_id = $1
order by created_at desc
limit 1;

-- name: MarkPaymentSettled :exec
update payments
set status = $2,
    provider_transaction_id = $3,
    raw_webhook_payload = $4,
    paid_at = case when $2 = 'paid' then now() else paid_at end
where provider_tx_ref = $1;
