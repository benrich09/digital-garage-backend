
-- request_kind: mechanic_request | garage_booking
alter table service_requests
  add column if not exists request_kind text not null default 'mechanic_request';

create index if not exists idx_sr_status_kind on service_requests (status, request_kind);

-- soft-hide for users (admin still sees all)
alter table service_requests
  add column if not exists hidden_by_owner boolean not null default false;
alter table service_requests
  add column if not exists hidden_by_provider boolean not null default false;
