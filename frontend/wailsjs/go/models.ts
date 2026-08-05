export namespace logs {

	export class Record {
	    seq: number;
	    ts: string;
	    message: string;
	    email?: string;
	    scope: string;
	    level: string;
	    module: string;

	    static createFrom(source: any = {}) {
	        return new Record(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.seq = source["seq"];
	        this.ts = source["ts"];
	        this.message = source["message"];
	        this.email = source["email"];
	        this.scope = source["scope"];
	        this.level = source["level"];
	        this.module = source["module"];
	    }
	}

}

export namespace providerproxy {

	export class Status {
	    enabled: boolean;
	    ready: number;
	    inflight: number;
	    target: number;
	    low_water: number;
	    failures: number;

	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.ready = source["ready"];
	        this.inflight = source["inflight"];
	        this.target = source["target"];
	        this.low_water = source["low_water"];
	        this.failures = source["failures"];
	    }
	}

}

export namespace settings {

	export class ProviderProxyConfig {
	    enabled: boolean;
	    username: string;
	    password: string;
	    endpoint: string;
	    duration: number;
	    regions: string;

	    static createFrom(source: any = {}) {
	        return new ProviderProxyConfig(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.endpoint = source["endpoint"];
	        this.duration = source["duration"];
	        this.regions = source["regions"];
	    }
	}
	export class Settings {
	    payment_mode: string;
	    target_amount: string;
	    headless: boolean;
	    local_proxy: string;
	    proxy_route_mode: string;
	    dynamic_proxies: string;
	    payment_dynamic_proxy: string;
	    followup_dynamic_proxy: string;
	    approve_dynamic_proxy: string;
	    reuse_payment_proxy: string;
	    reuse_followup_proxy: string;
	    reuse_approve_proxy: string;
	    link_proxy_region: string;
	    require_japan_extract_proxy: boolean;
	    register_with_payment_proxy: boolean;
	    force_legacy_paypal: boolean;
	    auth_concurrency: number;
	    k12_concurrency: number;
	    link_race_concurrency: number;
	    link_proxy_precheck_limit: number;
	    link_proxy_precheck_concurrency: number;
	    link_attempt_limit: number;
	    provider_proxy_configs: Record<string, ProviderProxyConfig>;
	    payment_extension_dir: string;
	    paypal_phone: string;
	    paypal_card: string;
	    paypal_sms_url: string;
	    paypal_phone_pool: string;
	    paypal_phone_pool_index: number;
	    export_name_prefix: string;
	    domain_mail_domain: string;
	    cloud_mail_enabled: boolean;
	    cloud_mail_base: string;
	    cloud_mail_token: string;
	    k12_workspace_id: string;
	    session_convert_format: string;
	    manual_email_otp: boolean;
	    phone_max_receive_count: number;
	    smsbower_enabled: boolean;
	    smsbower_api_key: string;
	    smsbower_service: string;
	    smsbower_country: string;
	    smsbower_max_price: string;
	    turnstile_solver_enabled: boolean;
	    turnstile_solver_url: string;
	    success_sound_enabled: boolean;
	    success_audio_device: string;
	    pause_others_on_link_success: boolean;
	    account_groups: string[];
	    account_group_filter: string;
	    account_status_filter: string;
	    workspace_page: string;
	    account_sort_column: string;
	    account_sort_direction: string;
	    window_geometry: string;
	    window_zoomed: boolean;
	    ui_layout_version: number;
	    main_sash_ratio: number;
	    log_sash_ratio: number;
	    body_sash_ratio: number;

	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.payment_mode = source["payment_mode"];
	        this.target_amount = source["target_amount"];
	        this.headless = source["headless"];
	        this.local_proxy = source["local_proxy"];
	        this.proxy_route_mode = source["proxy_route_mode"];
	        this.dynamic_proxies = source["dynamic_proxies"];
	        this.payment_dynamic_proxy = source["payment_dynamic_proxy"];
	        this.followup_dynamic_proxy = source["followup_dynamic_proxy"];
	        this.approve_dynamic_proxy = source["approve_dynamic_proxy"];
	        this.reuse_payment_proxy = source["reuse_payment_proxy"];
	        this.reuse_followup_proxy = source["reuse_followup_proxy"];
	        this.reuse_approve_proxy = source["reuse_approve_proxy"];
	        this.link_proxy_region = source["link_proxy_region"];
	        this.require_japan_extract_proxy = source["require_japan_extract_proxy"];
	        this.register_with_payment_proxy = source["register_with_payment_proxy"];
	        this.force_legacy_paypal = source["force_legacy_paypal"];
	        this.auth_concurrency = source["auth_concurrency"];
	        this.k12_concurrency = source["k12_concurrency"];
	        this.link_race_concurrency = source["link_race_concurrency"];
	        this.link_proxy_precheck_limit = source["link_proxy_precheck_limit"];
	        this.link_proxy_precheck_concurrency = source["link_proxy_precheck_concurrency"];
	        this.link_attempt_limit = source["link_attempt_limit"];
	        this.provider_proxy_configs = this.convertValues(source["provider_proxy_configs"], ProviderProxyConfig, true);
	        this.payment_extension_dir = source["payment_extension_dir"];
	        this.paypal_phone = source["paypal_phone"];
	        this.paypal_card = source["paypal_card"];
	        this.paypal_sms_url = source["paypal_sms_url"];
	        this.paypal_phone_pool = source["paypal_phone_pool"];
	        this.paypal_phone_pool_index = source["paypal_phone_pool_index"];
	        this.export_name_prefix = source["export_name_prefix"];
	        this.domain_mail_domain = source["domain_mail_domain"];
	        this.cloud_mail_enabled = source["cloud_mail_enabled"];
	        this.cloud_mail_base = source["cloud_mail_base"];
	        this.cloud_mail_token = source["cloud_mail_token"];
	        this.k12_workspace_id = source["k12_workspace_id"];
	        this.session_convert_format = source["session_convert_format"];
	        this.manual_email_otp = source["manual_email_otp"];
	        this.phone_max_receive_count = source["phone_max_receive_count"];
	        this.smsbower_enabled = source["smsbower_enabled"];
	        this.smsbower_api_key = source["smsbower_api_key"];
	        this.smsbower_service = source["smsbower_service"];
	        this.smsbower_country = source["smsbower_country"];
	        this.smsbower_max_price = source["smsbower_max_price"];
	        this.turnstile_solver_enabled = source["turnstile_solver_enabled"];
	        this.turnstile_solver_url = source["turnstile_solver_url"];
	        this.success_sound_enabled = source["success_sound_enabled"];
	        this.success_audio_device = source["success_audio_device"];
	        this.pause_others_on_link_success = source["pause_others_on_link_success"];
	        this.account_groups = source["account_groups"];
	        this.account_group_filter = source["account_group_filter"];
	        this.account_status_filter = source["account_status_filter"];
	        this.workspace_page = source["workspace_page"];
	        this.account_sort_column = source["account_sort_column"];
	        this.account_sort_direction = source["account_sort_direction"];
	        this.window_geometry = source["window_geometry"];
	        this.window_zoomed = source["window_zoomed"];
	        this.ui_layout_version = source["ui_layout_version"];
	        this.main_sash_ratio = source["main_sash_ratio"];
	        this.log_sash_ratio = source["log_sash_ratio"];
	        this.body_sash_ratio = source["body_sash_ratio"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace ui {

	export class AccountDetailAccount {
	    email: string;
	    password: string;
	    client_id: string;
	    refresh_token: string;
	    raw: string;
	    account_type: string;
	    status: string;
	    openai_rt: string;
	    auth_phone_number: string;
	    auth_phone_sms_url: string;
	    receive_mailbox: string;
	    mail_provider: string;
	    cloud_mail_base: string;
	    cloud_mail_token: string;
	    group: string;
	    browser_fingerprint: Record<string, any>;

	    static createFrom(source: any = {}) {
	        return new AccountDetailAccount(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.password = source["password"];
	        this.client_id = source["client_id"];
	        this.refresh_token = source["refresh_token"];
	        this.raw = source["raw"];
	        this.account_type = source["account_type"];
	        this.status = source["status"];
	        this.openai_rt = source["openai_rt"];
	        this.auth_phone_number = source["auth_phone_number"];
	        this.auth_phone_sms_url = source["auth_phone_sms_url"];
	        this.receive_mailbox = source["receive_mailbox"];
	        this.mail_provider = source["mail_provider"];
	        this.cloud_mail_base = source["cloud_mail_base"];
	        this.cloud_mail_token = source["cloud_mail_token"];
	        this.group = source["group"];
	        this.browser_fingerprint = source["browser_fingerprint"];
	    }
	}
	export class WorkflowEntry {
	    state: string;
	    detail: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new WorkflowEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.detail = source["detail"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class AccountDetails {
	    account: AccountDetailAccount;
	    session: Record<string, any>;
	    workflow: Record<string, WorkflowEntry>;
	    fingerprint: Record<string, any>;
	    link: string;
	    linkProxy: string;
	    linkProxyLabel: string;
	    linkProxyExit: string;
	    linkCreateProxy: string;
	    linkCreateProxyLabel: string;
	    linkCreateProxyExit: string;
	    linkFollowupProxy: string;
	    linkFollowupProxyLabel: string;
	    linkFollowupProxyExit: string;
	    linkApproveProxy: string;
	    linkApproveProxyLabel: string;
	    linkApproveProxyExit: string;
	    logs: logs.Record[];

	    static createFrom(source: any = {}) {
	        return new AccountDetails(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.account = this.convertValues(source["account"], AccountDetailAccount);
	        this.session = source["session"];
	        this.workflow = this.convertValues(source["workflow"], WorkflowEntry, true);
	        this.fingerprint = source["fingerprint"];
	        this.link = source["link"];
	        this.linkProxy = source["linkProxy"];
	        this.linkProxyLabel = source["linkProxyLabel"];
	        this.linkProxyExit = source["linkProxyExit"];
	        this.linkCreateProxy = source["linkCreateProxy"];
	        this.linkCreateProxyLabel = source["linkCreateProxyLabel"];
	        this.linkCreateProxyExit = source["linkCreateProxyExit"];
	        this.linkFollowupProxy = source["linkFollowupProxy"];
	        this.linkFollowupProxyLabel = source["linkFollowupProxyLabel"];
	        this.linkFollowupProxyExit = source["linkFollowupProxyExit"];
	        this.linkApproveProxy = source["linkApproveProxy"];
	        this.linkApproveProxyLabel = source["linkApproveProxyLabel"];
	        this.linkApproveProxyExit = source["linkApproveProxyExit"];
	        this.logs = this.convertValues(source["logs"], logs.Record);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AccountFilter {
	    group: string;
	    status: string;
	    search: string;
	    sortColumn: string;
	    sortDirection: string;
	    offset: number;
	    limit: number;

	    static createFrom(source: any = {}) {
	        return new AccountFilter(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.group = source["group"];
	        this.status = source["status"];
	        this.search = source["search"];
	        this.sortColumn = source["sortColumn"];
	        this.sortDirection = source["sortDirection"];
	        this.offset = source["offset"];
	        this.limit = source["limit"];
	    }
	}
	export class AccountMutationResult {
	    updated: number;
	    deleted: number;
	    total: number;
	    emails: string[];
	    message: string;

	    static createFrom(source: any = {}) {
	        return new AccountMutationResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.updated = source["updated"];
	        this.deleted = source["deleted"];
	        this.total = source["total"];
	        this.emails = source["emails"];
	        this.message = source["message"];
	    }
	}
	export class AccountRow {
	    email: string;
	    password: string;
	    client_id: string;
	    refresh_token: string;
	    raw: string;
	    account_type: string;
	    status: string;
	    openai_rt: string;
	    auth_phone_number: string;
	    auth_phone_sms_url: string;
	    receive_mailbox: string;
	    mail_provider: string;
	    group: string;
	    key: string;
	    statusText: string;
	    attempts: number;
	    hasSession: boolean;
	    link: string;

	    static createFrom(source: any = {}) {
	        return new AccountRow(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.password = source["password"];
	        this.client_id = source["client_id"];
	        this.refresh_token = source["refresh_token"];
	        this.raw = source["raw"];
	        this.account_type = source["account_type"];
	        this.status = source["status"];
	        this.openai_rt = source["openai_rt"];
	        this.auth_phone_number = source["auth_phone_number"];
	        this.auth_phone_sms_url = source["auth_phone_sms_url"];
	        this.receive_mailbox = source["receive_mailbox"];
	        this.mail_provider = source["mail_provider"];
	        this.group = source["group"];
	        this.key = source["key"];
	        this.statusText = source["statusText"];
	        this.attempts = source["attempts"];
	        this.hasSession = source["hasSession"];
	        this.link = source["link"];
	    }
	}
	export class AccountPage {
	    rows: AccountRow[];
	    total: number;
	    matched: number;
	    offset: number;
	    groups: string[];

	    static createFrom(source: any = {}) {
	        return new AccountPage(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rows = this.convertValues(source["rows"], AccountRow);
	        this.total = source["total"];
	        this.matched = source["matched"];
	        this.offset = source["offset"];
	        this.groups = source["groups"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class AuthBatchRequest {
	    emails: string[];
	    confirmed: boolean;

	    static createFrom(source: any = {}) {
	        return new AuthBatchRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emails = source["emails"];
	        this.confirmed = source["confirmed"];
	    }
	}
	export class AutoClassifyRequest {
	    mode: string;
	    scope: string;
	    selectedEmails: string[];
	    currentGroup: string;
	    currentStatus: string;
	    currentSearch: string;

	    static createFrom(source: any = {}) {
	        return new AutoClassifyRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.scope = source["scope"];
	        this.selectedEmails = source["selectedEmails"];
	        this.currentGroup = source["currentGroup"];
	        this.currentStatus = source["currentStatus"];
	        this.currentSearch = source["currentSearch"];
	    }
	}
	export class AutoClassifyResult {
	    updated: number;
	    counts: Record<string, number>;
	    groupFilter: string;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new AutoClassifyResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.updated = source["updated"];
	        this.counts = source["counts"];
	        this.groupFilter = source["groupFilter"];
	        this.message = source["message"];
	    }
	}
	export class BatchRelinkRequest {
	    emails: string[];
	    confirmed: boolean;

	    static createFrom(source: any = {}) {
	        return new BatchRelinkRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emails = source["emails"];
	        this.confirmed = source["confirmed"];
	    }
	}
	export class JobView {
	    id: string;
	    kind: string;
	    email: string;
	    status: string;
	    error: string;
	    started: string;
	    finished: string;
	    batchId: string;
	    total: number;
	    done: number;

	    static createFrom(source: any = {}) {
	        return new JobView(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.email = source["email"];
	        this.status = source["status"];
	        this.error = source["error"];
	        this.started = source["started"];
	        this.finished = source["finished"];
	        this.batchId = source["batchId"];
	        this.total = source["total"];
	        this.done = source["done"];
	    }
	}
	export class BatchRelinkSummary {
	    job: JobView;
	    skipped: string[];

	    static createFrom(source: any = {}) {
	        return new BatchRelinkSummary(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.job = this.convertValues(source["job"], JobView);
	        this.skipped = source["skipped"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class BatchSummary {
	    job: JobView;
	    skipped: string[];

	    static createFrom(source: any = {}) {
	        return new BatchSummary(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.job = this.convertValues(source["job"], JobView);
	        this.skipped = source["skipped"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CloudMailProbeRequest {
	    baseUrl: string;
	    token: string;
	    probeEmail: string;

	    static createFrom(source: any = {}) {
	        return new CloudMailProbeRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.baseUrl = source["baseUrl"];
	        this.token = source["token"];
	        this.probeEmail = source["probeEmail"];
	    }
	}
	export class CloudMailTokenRequest {
	    baseUrl: string;
	    adminEmail: string;
	    adminPassword: string;
	    confirmInvalidate: boolean;

	    static createFrom(source: any = {}) {
	        return new CloudMailTokenRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.baseUrl = source["baseUrl"];
	        this.adminEmail = source["adminEmail"];
	        this.adminPassword = source["adminPassword"];
	        this.confirmInvalidate = source["confirmInvalidate"];
	    }
	}
	export class DeactivationScanRequest {
	    email: string;
	    days: number;
	    maxMessagesPerFolder: number;

	    static createFrom(source: any = {}) {
	        return new DeactivationScanRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.days = source["days"];
	        this.maxMessagesPerFolder = source["maxMessagesPerFolder"];
	    }
	}
	export class DomainMailRequest {
	    selectedEmails: string[];
	    count: number;
	    receiveMailboxes: Record<string, string>;

	    static createFrom(source: any = {}) {
	        return new DomainMailRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.selectedEmails = source["selectedEmails"];
	        this.count = source["count"];
	        this.receiveMailboxes = source["receiveMailboxes"];
	    }
	}
	export class DomainRandomRTRequest {
	    confirmed: boolean;

	    static createFrom(source: any = {}) {
	        return new DomainRandomRTRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.confirmed = source["confirmed"];
	    }
	}
	export class DomainRandomRTResult {
	    email: string;
	    job: JobView;

	    static createFrom(source: any = {}) {
	        return new DomainRandomRTResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.job = this.convertValues(source["job"], JobView);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Env {
	    goVersion: string;
	    stateFile: string;
	    dataDir: string;
	    stateOK: boolean;

	    static createFrom(source: any = {}) {
	        return new Env(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.goVersion = source["goVersion"];
	        this.stateFile = source["stateFile"];
	        this.dataDir = source["dataDir"];
	        this.stateOK = source["stateOK"];
	    }
	}
	export class ExportPreview {
	    kind: string;
	    title: string;
	    text: string;
	    suggestedName: string;
	    count: number;
	    skipped: string[];
	    skippedNote: string;
	    entries: string[];

	    static createFrom(source: any = {}) {
	        return new ExportPreview(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.title = source["title"];
	        this.text = source["text"];
	        this.suggestedName = source["suggestedName"];
	        this.count = source["count"];
	        this.skipped = source["skipped"];
	        this.skippedNote = source["skippedNote"];
	        this.entries = source["entries"];
	    }
	}
	export class ExportResult {
	    kind: string;
	    path: string;
	    cancelled: boolean;
	    bytes: number;
	    count: number;
	    skipped: string[];
	    skippedNote: string;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new ExportResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.path = source["path"];
	        this.cancelled = source["cancelled"];
	        this.bytes = source["bytes"];
	        this.count = source["count"];
	        this.skipped = source["skipped"];
	        this.skippedNote = source["skippedNote"];
	        this.message = source["message"];
	    }
	}
	export class ExternalOAuthRequest {
	    email: string;
	    url: string;
	    confirmedNonStandard: boolean;

	    static createFrom(source: any = {}) {
	        return new ExternalOAuthRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.url = source["url"];
	        this.confirmedNonStandard = source["confirmedNonStandard"];
	    }
	}
	export class GenerateLinksRequest {
	    email: string;
	    confirmed: boolean;

	    static createFrom(source: any = {}) {
	        return new GenerateLinksRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.confirmed = source["confirmed"];
	    }
	}
	export class GeneratedAccountsResult {
	    created: number;
	    total: number;
	    emails: string[];
	    errors: string[];
	    group: string;
	    cloudMode: boolean;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new GeneratedAccountsResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.created = source["created"];
	        this.total = source["total"];
	        this.emails = source["emails"];
	        this.errors = source["errors"];
	        this.group = source["group"];
	        this.cloudMode = source["cloudMode"];
	        this.message = source["message"];
	    }
	}
	export class GroupMutationResult {
	    group: string;
	    previousGroup: string;
	    updated: number;
	    groups: string[];

	    static createFrom(source: any = {}) {
	        return new GroupMutationResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.group = source["group"];
	        this.previousGroup = source["previousGroup"];
	        this.updated = source["updated"];
	        this.groups = source["groups"];
	    }
	}
	export class ImportResult {
	    imported: number;
	    added: number;
	    updated: number;
	    total: number;
	    group: string;
	    errors: string[];
	    message: string;

	    static createFrom(source: any = {}) {
	        return new ImportResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.imported = source["imported"];
	        this.added = source["added"];
	        this.updated = source["updated"];
	        this.total = source["total"];
	        this.group = source["group"];
	        this.errors = source["errors"];
	        this.message = source["message"];
	    }
	}

	export class K12InviteFlowRequest {
	    emails: string[];
	    workspaceId: string;
	    confirmed: boolean;

	    static createFrom(source: any = {}) {
	        return new K12InviteFlowRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emails = source["emails"];
	        this.workspaceId = source["workspaceId"];
	        this.confirmed = source["confirmed"];
	    }
	}
	export class K12RequestInviteRequest {
	    email: string;
	    workspaceId: string;

	    static createFrom(source: any = {}) {
	        return new K12RequestInviteRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.workspaceId = source["workspaceId"];
	    }
	}
	export class LinkBatchSummary {
	    job: JobView;
	    skipped: string[];

	    static createFrom(source: any = {}) {
	        return new LinkBatchSummary(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.job = this.convertValues(source["job"], JobView);
	        this.skipped = source["skipped"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogSnapshot {
	    accountTitle: string;
	    account: logs.Record[];
	    global: logs.Record[];

	    static createFrom(source: any = {}) {
	        return new LogSnapshot(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountTitle = source["accountTitle"];
	        this.account = this.convertValues(source["account"], logs.Record);
	        this.global = this.convertValues(source["global"], logs.Record);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MailboxMessage {
	    id: string;
	    folder: string;
	    kind: string;
	    code: string;
	    subject: string;
	    from: string;
	    to: string;
	    date: string;
	    mailTime: number;
	    mailTimeIso: string;
	    snippet: string;
	    body?: string;

	    static createFrom(source: any = {}) {
	        return new MailboxMessage(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.folder = source["folder"];
	        this.kind = source["kind"];
	        this.code = source["code"];
	        this.subject = source["subject"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.date = source["date"];
	        this.mailTime = source["mailTime"];
	        this.mailTimeIso = source["mailTimeIso"];
	        this.snippet = source["snippet"];
	        this.body = source["body"];
	    }
	}
	export class MailboxMessageRequest {
	    email: string;
	    folder: string;
	    id: string;

	    static createFrom(source: any = {}) {
	        return new MailboxMessageRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.folder = source["folder"];
	        this.id = source["id"];
	    }
	}
	export class MailboxMessagesRequest {
	    email: string;
	    folder: string;
	    limit: number;
	    query: string;

	    static createFrom(source: any = {}) {
	        return new MailboxMessagesRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.folder = source["folder"];
	        this.limit = source["limit"];
	        this.query = source["query"];
	    }
	}
	export class MissingRTView {
	    emails: string[];
	    prompt: string;

	    static createFrom(source: any = {}) {
	        return new MissingRTView(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emails = source["emails"];
	        this.prompt = source["prompt"];
	    }
	}
	export class NetworkJobResult {
	    job: JobView;
	    result: any;

	    static createFrom(source: any = {}) {
	        return new NetworkJobResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.job = this.convertValues(source["job"], JobView);
	        this.result = source["result"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class OpenPaymentWindowRequest {
	    email: string;
	    proxyMode: string;
	    confirmed: boolean;
	    autoConfirm: boolean;
	    confirmAutoCharge: boolean;

	    static createFrom(source: any = {}) {
	        return new OpenPaymentWindowRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.proxyMode = source["proxyMode"];
	        this.confirmed = source["confirmed"];
	        this.autoConfirm = source["autoConfirm"];
	        this.confirmAutoCharge = source["confirmAutoCharge"];
	    }
	}
	export class OpenPaymentWindowsRequest {
	    emails: string[];
	    confirmed: boolean;
	    autoConfirm: boolean;
	    confirmAutoCharge: boolean;

	    static createFrom(source: any = {}) {
	        return new OpenPaymentWindowsRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emails = source["emails"];
	        this.confirmed = source["confirmed"];
	        this.autoConfirm = source["autoConfirm"];
	        this.confirmAutoCharge = source["confirmAutoCharge"];
	    }
	}
	export class PaymentWindowSkip {
	    email: string;
	    error: string;

	    static createFrom(source: any = {}) {
	        return new PaymentWindowSkip(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.error = source["error"];
	    }
	}
	export class OpenPaymentWindowsResult {
	    jobs: JobView[];
	    skipped: PaymentWindowSkip[];

	    static createFrom(source: any = {}) {
	        return new OpenPaymentWindowsResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.jobs = this.convertValues(source["jobs"], JobView);
	        this.skipped = this.convertValues(source["skipped"], PaymentWindowSkip);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PaymentCardView {
	    card: string;
	    month: string;
	    year: string;
	    cvv: string;
	    status: string;

	    static createFrom(source: any = {}) {
	        return new PaymentCardView(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.card = source["card"];
	        this.month = source["month"];
	        this.year = source["year"];
	        this.cvv = source["cvv"];
	        this.status = source["status"];
	    }
	}
	export class PaymentCardConsumeResult {
	    cardText: string;
	    card: PaymentCardView;
	    fromPool: boolean;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new PaymentCardConsumeResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cardText = source["cardText"];
	        this.card = this.convertValues(source["card"], PaymentCardView);
	        this.fromPool = source["fromPool"];
	        this.message = source["message"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class PaymentCardsResult {
	    imported: number;
	    updated: number;
	    total: number;
	    errors: string[];
	    cards: PaymentCardView[];
	    message: string;

	    static createFrom(source: any = {}) {
	        return new PaymentCardsResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.imported = source["imported"];
	        this.updated = source["updated"];
	        this.total = source["total"];
	        this.errors = source["errors"];
	        this.cards = this.convertValues(source["cards"], PaymentCardView);
	        this.message = source["message"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PaymentProxySwitchResult {
	    email: string;
	    proxy: string;

	    static createFrom(source: any = {}) {
	        return new PaymentProxySwitchResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.proxy = source["proxy"];
	    }
	}

	export class PhoneView {
	    number: string;
	    smsUrl: string;
	    receiveCount: number;
	    status: string;
	    lastCode: string;
	    lastError: string;

	    static createFrom(source: any = {}) {
	        return new PhoneView(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.number = source["number"];
	        this.smsUrl = source["smsUrl"];
	        this.receiveCount = source["receiveCount"];
	        this.status = source["status"];
	        this.lastCode = source["lastCode"];
	        this.lastError = source["lastError"];
	    }
	}
	export class PhonesResult {
	    imported: number;
	    updated: number;
	    total: number;
	    errors: string[];
	    phones: PhoneView[];
	    message: string;

	    static createFrom(source: any = {}) {
	        return new PhonesResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.imported = source["imported"];
	        this.updated = source["updated"];
	        this.total = source["total"];
	        this.errors = source["errors"];
	        this.phones = this.convertValues(source["phones"], PhoneView);
	        this.message = source["message"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PlusAliasRequest {
	    emails: string[];
	    count: number;

	    static createFrom(source: any = {}) {
	        return new PlusAliasRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emails = source["emails"];
	        this.count = source["count"];
	    }
	}
	export class ProviderProxyStatusView {
	    role: string;
	    label: string;
	    config: settings.ProviderProxyConfig;
	    status: providerproxy.Status;
	    text: string;

	    static createFrom(source: any = {}) {
	        return new ProviderProxyStatusView(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.role = source["role"];
	        this.label = source["label"];
	        this.config = this.convertValues(source["config"], settings.ProviderProxyConfig);
	        this.status = this.convertValues(source["status"], providerproxy.Status);
	        this.text = source["text"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ProxyPoolOperationRequest {
	    confirmed: boolean;

	    static createFrom(source: any = {}) {
	        return new ProxyPoolOperationRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.confirmed = source["confirmed"];
	    }
	}
	export class RefreshAccountTypeRequest {
	    email: string;

	    static createFrom(source: any = {}) {
	        return new RefreshAccountTypeRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	    }
	}
	export class SMSBowerReadRequest {
	    apiKey: string;
	    service: string;
	    country: string;
	    includePrices: boolean;

	    static createFrom(source: any = {}) {
	        return new SMSBowerReadRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.apiKey = source["apiKey"];
	        this.service = source["service"];
	        this.country = source["country"];
	        this.includePrices = source["includePrices"];
	    }
	}
	export class SessionRefreshRequest {
	    email: string;
	    k12: boolean;
	    workspaceId: string;

	    static createFrom(source: any = {}) {
	        return new SessionRefreshRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.k12 = source["k12"];
	        this.workspaceId = source["workspaceId"];
	    }
	}
	export class SessionSaveResult {
	    email: string;
	    planType: string;
	    status: string;
	    created: boolean;
	    summary: Record<string, any>;
	    sessionJson: string;

	    static createFrom(source: any = {}) {
	        return new SessionSaveResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.planType = source["planType"];
	        this.status = source["status"];
	        this.created = source["created"];
	        this.summary = source["summary"];
	        this.sessionJson = source["sessionJson"];
	    }
	}
	export class StartBatchRequest {
	    emails: string[];
	    confirmed: boolean;
	    collectSession: boolean;

	    static createFrom(source: any = {}) {
	        return new StartBatchRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emails = source["emails"];
	        this.confirmed = source["confirmed"];
	        this.collectSession = source["collectSession"];
	    }
	}
	export class StartLinkBatchRequest {
	    emails: string[];
	    confirmed: boolean;

	    static createFrom(source: any = {}) {
	        return new StartLinkBatchRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emails = source["emails"];
	        this.confirmed = source["confirmed"];
	    }
	}
	export class StartRegisterRequest {
	    email: string;
	    confirmed: boolean;
	    collectSession: boolean;

	    static createFrom(source: any = {}) {
	        return new StartRegisterRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.confirmed = source["confirmed"];
	        this.collectSession = source["collectSession"];
	    }
	}
	export class StateSummary {
	    accounts: number;
	    sessions: number;
	    settingsKeys: string[];
	    schemaFile: string;

	    static createFrom(source: any = {}) {
	        return new StateSummary(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accounts = source["accounts"];
	        this.sessions = source["sessions"];
	        this.settingsKeys = source["settingsKeys"];
	        this.schemaFile = source["schemaFile"];
	    }
	}
	export class Sub2APIPlan {
	    emails: string[];
	    exportEmails: string[];
	    missingEmails: string[];
	    authorizationPrompt: string;

	    static createFrom(source: any = {}) {
	        return new Sub2APIPlan(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emails = source["emails"];
	        this.exportEmails = source["exportEmails"];
	        this.missingEmails = source["missingEmails"];
	        this.authorizationPrompt = source["authorizationPrompt"];
	    }
	}
	export class Sub2APISaveResult {
	    path: string;
	    cancelled: boolean;
	    bytes: number;
	    count: number;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new Sub2APISaveResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.cancelled = source["cancelled"];
	        this.bytes = source["bytes"];
	        this.count = source["count"];
	        this.message = source["message"];
	    }
	}
	export class TeamInviteRequest {
	    email: string;
	    targetEmail: string;
	    confirmBillableSeat: boolean;

	    static createFrom(source: any = {}) {
	        return new TeamInviteRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.targetEmail = source["targetEmail"];
	        this.confirmBillableSeat = source["confirmBillableSeat"];
	    }
	}
	export class TeamInviteScanJoinRequest {
	    emails: string[];
	    confirmed: boolean;

	    static createFrom(source: any = {}) {
	        return new TeamInviteScanJoinRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emails = source["emails"];
	        this.confirmed = source["confirmed"];
	    }
	}
	export class TeamLeaveRequest {
	    email: string;
	    confirmed: boolean;

	    static createFrom(source: any = {}) {
	        return new TeamLeaveRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.confirmed = source["confirmed"];
	    }
	}
	export class TrialEligibilityRequest {
	    email: string;
	    country: string;
	    confirmCheckout: boolean;

	    static createFrom(source: any = {}) {
	        return new TrialEligibilityRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.country = source["country"];
	        this.confirmCheckout = source["confirmCheckout"];
	    }
	}
	export class TurnstileProbeRequest {
	    url: string;

	    static createFrom(source: any = {}) {
	        return new TurnstileProbeRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	    }
	}
	export class WorkflowClearResult {
	    email: string;
	    changed: boolean;

	    static createFrom(source: any = {}) {
	        return new WorkflowClearResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.changed = source["changed"];
	    }
	}

	export class WorkspaceInviteRequest {
	    email: string;
	    kind: string;
	    inviteUrl: string;
	    workspaceId: string;
	    refreshSession: boolean;
	    confirmed: boolean;

	    static createFrom(source: any = {}) {
	        return new WorkspaceInviteRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.kind = source["kind"];
	        this.inviteUrl = source["inviteUrl"];
	        this.workspaceId = source["workspaceId"];
	        this.refreshSession = source["refreshSession"];
	        this.confirmed = source["confirmed"];
	    }
	}

}
