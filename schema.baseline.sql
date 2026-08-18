--
-- PostgreSQL database dump
--



SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: authz_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.authz_events (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    organization_id uuid,
    actor_id uuid NOT NULL,
    subject_id uuid,
    action character varying(40) NOT NULL,
    role_id uuid,
    permission_key character varying(100),
    ip inet NOT NULL,
    user_agent character varying(512),
    detail character varying(500)
);


--
-- Name: devices; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.devices (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    user_id uuid NOT NULL,
    fingerprint character varying(64) NOT NULL,
    label character varying(100),
    user_agent character varying(512),
    last_seen_at timestamp with time zone,
    last_ip inet,
    trusted_at timestamp with time zone,
    revoked_at timestamp with time zone
);


--
-- Name: email_changes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_changes (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    user_id uuid NOT NULL,
    new_email character varying(255) NOT NULL,
    code_hash character varying(64) NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    attempts bigint NOT NULL,
    consumed_at timestamp with time zone
);


--
-- Name: invitation_roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.invitation_roles (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    invitation_id uuid NOT NULL,
    role_id uuid NOT NULL
);


--
-- Name: invitations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.invitations (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    organization_id uuid NOT NULL,
    email character varying(255) NOT NULL,
    token_hash character varying(64) NOT NULL,
    invited_by uuid,
    expires_at timestamp with time zone NOT NULL,
    accepted_at timestamp with time zone
);


--
-- Name: login_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.login_events (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    user_id uuid NOT NULL,
    device_id uuid,
    ip inet NOT NULL,
    user_agent character varying(512),
    outcome character varying(20) NOT NULL,
    country character varying(2),
    CONSTRAINT chk_login_events_outcome CHECK (((outcome)::text = ANY (ARRAY[('success'::character varying)::text, ('bad_password'::character varying)::text, ('mfa_failed'::character varying)::text, ('locked'::character varying)::text])))
);


--
-- Name: membership_roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.membership_roles (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    membership_id uuid NOT NULL,
    role_id uuid NOT NULL,
    granted_by uuid
);


--
-- Name: memberships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.memberships (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    user_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    status character varying(20) NOT NULL,
    invited_by uuid,
    joined_at timestamp with time zone,
    CONSTRAINT chk_memberships_status CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('suspended'::character varying)::text])))
);


--
-- Name: organizations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.organizations (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    deleted_at timestamp with time zone,
    is_protected boolean,
    slug character varying(63) NOT NULL,
    name character varying(100) NOT NULL
);


--
-- Name: password_resets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.password_resets (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    user_id uuid NOT NULL,
    code_hash character varying(64) NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    attempts bigint NOT NULL,
    consumed_at timestamp with time zone
);


--
-- Name: role_permissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.role_permissions (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    role_id uuid NOT NULL,
    permission_key character varying(100) NOT NULL
);


--
-- Name: roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.roles (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    organization_id uuid NOT NULL,
    key character varying(64) NOT NULL,
    name character varying(100) NOT NULL,
    description character varying(255),
    is_system boolean DEFAULT false NOT NULL
);


--
-- Name: two_factor_challenges; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.two_factor_challenges (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    user_id uuid NOT NULL,
    device_id uuid NOT NULL,
    code_hash character varying(64) NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    attempts bigint NOT NULL,
    consumed_at timestamp with time zone
);


--
-- Name: user_system_roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_system_roles (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    user_id uuid NOT NULL,
    role_key character varying(64) NOT NULL,
    granted_by uuid
);


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id uuid NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    deleted_at timestamp with time zone,
    is_protected boolean,
    name character varying(100) NOT NULL,
    email character varying(255) NOT NULL,
    password_hash character varying(255) NOT NULL,
    session_epoch bigint DEFAULT 0 NOT NULL,
    two_factor_enabled boolean DEFAULT false NOT NULL,
    suspended_at timestamp with time zone,
    locale character varying(10)
);


--
-- Name: authz_events authz_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.authz_events
    ADD CONSTRAINT authz_events_pkey PRIMARY KEY (id);


--
-- Name: devices devices_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.devices
    ADD CONSTRAINT devices_pkey PRIMARY KEY (id);


--
-- Name: email_changes email_changes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_changes
    ADD CONSTRAINT email_changes_pkey PRIMARY KEY (id);


--
-- Name: invitation_roles invitation_roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invitation_roles
    ADD CONSTRAINT invitation_roles_pkey PRIMARY KEY (id);


--
-- Name: invitations invitations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invitations
    ADD CONSTRAINT invitations_pkey PRIMARY KEY (id);


--
-- Name: login_events login_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.login_events
    ADD CONSTRAINT login_events_pkey PRIMARY KEY (id);


--
-- Name: membership_roles membership_roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.membership_roles
    ADD CONSTRAINT membership_roles_pkey PRIMARY KEY (id);


--
-- Name: memberships memberships_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT memberships_pkey PRIMARY KEY (id);


--
-- Name: organizations organizations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organizations
    ADD CONSTRAINT organizations_pkey PRIMARY KEY (id);


--
-- Name: password_resets password_resets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_resets
    ADD CONSTRAINT password_resets_pkey PRIMARY KEY (id);


--
-- Name: role_permissions role_permissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_pkey PRIMARY KEY (id);


--
-- Name: roles roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_pkey PRIMARY KEY (id);


--
-- Name: two_factor_challenges two_factor_challenges_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.two_factor_challenges
    ADD CONSTRAINT two_factor_challenges_pkey PRIMARY KEY (id);


--
-- Name: user_system_roles user_system_roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_system_roles
    ADD CONSTRAINT user_system_roles_pkey PRIMARY KEY (id);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: idx_authz_actor_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_authz_actor_time ON public.authz_events USING btree (actor_id, created_at);


--
-- Name: idx_authz_events_subject_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_authz_events_subject_id ON public.authz_events USING btree (subject_id);


--
-- Name: idx_authz_org_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_authz_org_time ON public.authz_events USING btree (organization_id, created_at);


--
-- Name: idx_device_user_fp; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_device_user_fp ON public.devices USING btree (user_id, fingerprint);


--
-- Name: idx_devices_last_seen_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_devices_last_seen_at ON public.devices USING btree (last_seen_at);


--
-- Name: idx_email_changes_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_changes_expires_at ON public.email_changes USING btree (expires_at);


--
-- Name: idx_email_changes_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_email_changes_user_id ON public.email_changes USING btree (user_id);


--
-- Name: idx_invitation_org_email; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_invitation_org_email ON public.invitations USING btree (organization_id, email);


--
-- Name: idx_invitation_role; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_invitation_role ON public.invitation_roles USING btree (invitation_id, role_id);


--
-- Name: idx_invitations_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_invitations_expires_at ON public.invitations USING btree (expires_at);


--
-- Name: idx_invitations_token_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_invitations_token_hash ON public.invitations USING btree (token_hash);


--
-- Name: idx_login_device_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_login_device_time ON public.login_events USING btree (device_id, created_at);


--
-- Name: idx_login_events_ip; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_login_events_ip ON public.login_events USING btree (ip);


--
-- Name: idx_login_user_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_login_user_time ON public.login_events USING btree (user_id, created_at);


--
-- Name: idx_membership_role; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_membership_role ON public.membership_roles USING btree (membership_id, role_id);


--
-- Name: idx_membership_user_org; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_membership_user_org ON public.memberships USING btree (user_id, organization_id);


--
-- Name: idx_organizations_deleted_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_organizations_deleted_at ON public.organizations USING btree (deleted_at);


--
-- Name: idx_organizations_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_organizations_slug ON public.organizations USING btree (slug) WHERE (deleted_at IS NULL);


--
-- Name: idx_password_resets_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_password_resets_expires_at ON public.password_resets USING btree (expires_at);


--
-- Name: idx_password_resets_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_password_resets_user_id ON public.password_resets USING btree (user_id);


--
-- Name: idx_role_org_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_role_org_key ON public.roles USING btree (organization_id, key);


--
-- Name: idx_role_permission; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_role_permission ON public.role_permissions USING btree (role_id, permission_key);


--
-- Name: idx_two_factor_challenges_device_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_two_factor_challenges_device_id ON public.two_factor_challenges USING btree (device_id);


--
-- Name: idx_two_factor_challenges_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_two_factor_challenges_expires_at ON public.two_factor_challenges USING btree (expires_at);


--
-- Name: idx_two_factor_challenges_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_two_factor_challenges_user_id ON public.two_factor_challenges USING btree (user_id);


--
-- Name: idx_user_system_role; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_user_system_role ON public.user_system_roles USING btree (user_id, role_key);


--
-- Name: idx_users_deleted_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_deleted_at ON public.users USING btree (deleted_at);


--
-- Name: idx_users_email; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_users_email ON public.users USING btree (email) WHERE (deleted_at IS NULL);


--
-- Name: email_changes fk_email_changes_user; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_changes
    ADD CONSTRAINT fk_email_changes_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: invitation_roles fk_invitation_roles_role; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invitation_roles
    ADD CONSTRAINT fk_invitation_roles_role FOREIGN KEY (role_id) REFERENCES public.roles(id) ON DELETE CASCADE;


--
-- Name: invitations fk_invitations_organization; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invitations
    ADD CONSTRAINT fk_invitations_organization FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: invitation_roles fk_invitations_roles; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invitation_roles
    ADD CONSTRAINT fk_invitations_roles FOREIGN KEY (invitation_id) REFERENCES public.invitations(id) ON DELETE CASCADE;


--
-- Name: membership_roles fk_membership_roles_role; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.membership_roles
    ADD CONSTRAINT fk_membership_roles_role FOREIGN KEY (role_id) REFERENCES public.roles(id) ON DELETE CASCADE;


--
-- Name: membership_roles fk_memberships_roles; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.membership_roles
    ADD CONSTRAINT fk_memberships_roles FOREIGN KEY (membership_id) REFERENCES public.memberships(id) ON DELETE CASCADE;


--
-- Name: memberships fk_memberships_user; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT fk_memberships_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: memberships fk_organizations_memberships; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT fk_organizations_memberships FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: roles fk_organizations_roles; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT fk_organizations_roles FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE;


--
-- Name: password_resets fk_password_resets_user; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_resets
    ADD CONSTRAINT fk_password_resets_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: role_permissions fk_roles_permissions; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT fk_roles_permissions FOREIGN KEY (role_id) REFERENCES public.roles(id) ON DELETE CASCADE;


--
-- Name: two_factor_challenges fk_two_factor_challenges_device; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.two_factor_challenges
    ADD CONSTRAINT fk_two_factor_challenges_device FOREIGN KEY (device_id) REFERENCES public.devices(id) ON DELETE CASCADE;


--
-- Name: two_factor_challenges fk_two_factor_challenges_user; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.two_factor_challenges
    ADD CONSTRAINT fk_two_factor_challenges_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_system_roles fk_user_system_roles_user; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_system_roles
    ADD CONSTRAINT fk_user_system_roles_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: devices fk_users_devices; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.devices
    ADD CONSTRAINT fk_users_devices FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: login_events fk_users_login_events; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.login_events
    ADD CONSTRAINT fk_users_login_events FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- PostgreSQL database dump complete
--


