-- name: UpsertDeviceToken :exec
insert into device_tokens (user_id, token, platform)
values ($1, $2, $3)
on conflict (token) do update
  set user_id = excluded.user_id,
      platform = excluded.platform,
      updated_at = now();

-- name: DeleteDeviceToken :exec
delete from device_tokens where token = $1;

-- name: ListDeviceTokensForUser :many
select token from device_tokens where user_id = $1;
