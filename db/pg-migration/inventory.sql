-- Object inventory of one database, normalized so that a PostgreSQL 13 source
-- and its PostgreSQL 17 copy produce identical output. Password hashes are
-- left out: the migration re-hashes them for scram-sha-256.
\pset pager off
create temp table _inv(line text);
create function pg_temp.acl(a aclitem[], owner name, deflt text) returns text language sql as $$
  -- PG17 adds the MAINTAIN privilege (m) to owners; an explicit ACL equal to
  -- the default reads back as NULL after pg_restore.
  select coalesce(nullif(
    (select string_agg(regexp_replace(x::text, 'm(?=[a-zA-Z*]*/)', '', 'g'), ',' order by x::text collate "C") from unnest(a) x),
    replace(deflt, '%', owner)), '')
$$;
insert into _inv select concat_ws('|', 'role', rolname, rolsuper, rolinherit, rolcreaterole, rolcreatedb, rolcanlogin, rolreplication, rolbypassrls, rolconnlimit, rolpassword is not null)
  from pg_authid where rolname !~ '^(pg_|cnpg_)' and rolname not in ('streaming_replica', 'postgres', 'app');
insert into _inv select concat_ws('|', 'rolesetting', coalesce(d.datname, '*'), r.rolname, s.setconfig::text)
  from pg_db_role_setting s left join pg_database d on d.oid = s.setdatabase join pg_roles r on r.oid = s.setrole
  where r.rolname !~ '^(pg_|cnpg_)' and r.rolname not in ('streaming_replica', 'postgres');
insert into _inv select concat_ws('|', 'member', r.rolname, m.rolname, a.admin_option)
  from pg_auth_members a join pg_roles r on r.oid = a.roleid join pg_roles m on m.oid = a.member
  where m.rolname !~ '^(pg_|cnpg_)' and m.rolname not in ('streaming_replica', 'postgres');
insert into _inv select concat_ws('|', 'database', datname, pg_get_userbyid(datdba), pg_encoding_to_char(encoding), datcollate, datctype)
  from pg_database where datname = current_database();
insert into _inv select concat_ws('|', 'extension', extname, extversion, extnamespace::regnamespace) from pg_extension;
insert into _inv select concat_ws('|', 'schema', nspname, pg_get_userbyid(nspowner)) from pg_namespace
  where nspname !~ '^pg_' and nspname not in ('information_schema', 'public');
insert into _inv select concat_ws('|', 'relation', n.nspname, c.relname, c.relkind, pg_get_userbyid(c.relowner),
       pg_temp.acl(c.relacl, pg_get_userbyid(c.relowner), case when c.relkind = 'S' then '%=rwU/%' else '%=arwdDxt/%' end),
       c.relrowsecurity, c.relforcerowsecurity)
  from pg_class c join pg_namespace n on n.oid = c.relnamespace
  where n.nspname !~ '^pg_' and n.nspname <> 'information_schema' and c.relkind in ('r', 'v', 'm', 'S', 'p', 'f', 'i');
insert into _inv select concat_ws('|', 'column', table_schema, table_name, column_name, data_type, coalesce(column_default, ''), is_nullable)
  from information_schema.columns where table_schema !~ '^pg_' and table_schema <> 'information_schema';
insert into _inv select concat_ws('|', 'constraint', n.nspname, cl.relname, c.conname, c.contype)
  from pg_constraint c join pg_class cl on cl.oid = c.conrelid join pg_namespace n on n.oid = cl.relnamespace
  where n.nspname !~ '^pg_' and n.nspname <> 'information_schema';
insert into _inv select concat_ws('|', 'function', n.nspname, p.proname, pg_get_function_identity_arguments(p.oid), pg_get_userbyid(p.proowner),
       pg_temp.acl(p.proacl, pg_get_userbyid(p.proowner), '=X/%,%=X/%'), p.prosecdef, coalesce(p.proconfig::text, ''), md5(p.prosrc))
  from pg_proc p join pg_namespace n on n.oid = p.pronamespace
  where n.nspname !~ '^pg_' and n.nspname <> 'information_schema';
insert into _inv select concat_ws('|', 'trigger', n.nspname, c.relname, t.tgname, t.tgenabled)
  from pg_trigger t join pg_class c on c.oid = t.tgrelid join pg_namespace n on n.oid = c.relnamespace where not t.tgisinternal;
insert into _inv select concat_ws('|', 'policy', schemaname, tablename, policyname, permissive, roles::text, cmd,
       md5(regexp_replace(coalesce(qual, ''), '\s+', ' ', 'g')), md5(regexp_replace(coalesce(with_check, ''), '\s+', ' ', 'g')))
  from pg_policies;
insert into _inv select concat_ws('|', 'defacl', pg_get_userbyid(defaclrole), coalesce(defaclnamespace::regnamespace::text, '*'), defaclobjtype,
       pg_temp.acl(defaclacl, pg_get_userbyid(defaclrole), ''))
  from pg_default_acl;
insert into _inv select concat_ws('|', 'sequence', schemaname, sequencename, coalesce(last_value::text, 'null')) from pg_sequences;
do $$
declare x record; cnt bigint; h text;
begin
  for x in select n.nspname s, c.relname r from pg_class c join pg_namespace n on n.oid = c.relnamespace
           where c.relkind in ('r', 'p') and n.nspname !~ '^pg_' and n.nspname <> 'information_schema' loop
    execute format('select count(*), md5(coalesce(string_agg(t::text, E''\n'' order by t::text collate "C"), '''')) from %I.%I t', x.s, x.r)
      into cnt, h;
    insert into _inv values (concat_ws('|', 'rows', x.s, x.r, cnt, h));
  end loop;
end $$;
select line from _inv order by line collate "C";
select 'END';
