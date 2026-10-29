# Railway 账号迁移手册（每月一次）

把 `fearless-luck` 整个项目（网关 + coincoin + 通联 + 长任务入口）从旧 Railway 账号搬到新账号。
做法是**在新账号重建项目 + 数据实时同步 + 逐个服务切换**，不用 Railway 的 project transfer。

> 第一次实操：2026-10-29。
> 下面的步骤、耗时和坑都来自那次实操；脚本在 `scripts/`，是那次跑通的脚本整理版。

---

## 0. 一页纸总览

| 阶段 | 做什么 | 影响线上吗 | 耗时 |
|---|---|---|---|
| A 盘点 | 导出旧项目配置、变量、卷、域名、数据库参数 | 否 | 2 分钟 |
| B 搭建 | 新项目建同名服务/卷，变量照搬，应用先"休眠"；Postgres 做成旧库的实时从库 | 否 | 10 分钟 |
| C 同步 | Postgres 流复制、Redis 主从、MySQL 预拷贝 | 否 | PG 25 GB 约 7 分钟；MySQL 视大小 |
| D 切换 | 网关 → 长任务入口 → 通联 → coincoin | **是，逐个短暂停服** | 每个 1～3 分钟 |
| E 验证 | 全部入口、副本、配置/参数对比、按分组测模型 | 否 | 5 分钟 |
| F 收尾 | 清理复制配置、移除旧服务、后台补历史日志 | 否 | 后台 |

**各服务预计停服**（按这份流程、Cloudflare token 提前备好时）：

| 服务 | 入口 | 预计停服 | 10-29 实际 |
|---|---|---|---|
| 网关 BirdSub2api | birdapi.up.railway.app | 约 2 分钟 | 2 分钟 |
| 通联专属 | datayes.up.railway.app | 约 1 分钟 | 1 分钟 |
| 通联专属 | datayes.coincoin.ai | 约 4 分钟（等 Railway 验证） | 约 15 分钟（等人工加 TXT） |
| coincoin-proxy | clawfather.up.railway.app | 约 2 分钟 | 23 分钟（脚本问题，已修） |
| coincoin-proxy | coincoin.ai | 约 4 分钟 | 19 分钟（等人工加 TXT） |
| 长任务入口 | api.birdbirdapi.com:端口 | 立即可用，**端口会变** | — |

**必须人工参与的只有两件事**：
1. 提前准备一个能改 **coincoin.ai** DNS 的 Cloudflare token，否则切换时要有人在线加 TXT 记录。
2. 切换后通知长任务客户**新端口**。

---

## 1. 迁移范围

| 服务 | 类型 | 数据怎么搬 | 对外入口 | 迁移后会变什么 |
|---|---|---|---|---|
| BirdSub2api | 网关（GitHub `hezhaoqian1/BirdSub2api` main，2 副本） | 无状态 | birdapi.up.railway.app | 不变（域名释放后新项目立即认领） |
| Postgres | 网关主库（约 25 GB） | **物理流复制**，切换时提升 | 内网 | 不变 |
| Redis | 网关缓存/并发槽 | **REPLICAOF** 主从 | 内网 | 不变 |
| caddy-long | 长任务入口（`deploy/railway-long-entry`，`railway up` 上传） | 证书自动重签 | api.birdbirdapi.com:**端口** | **TCP 端口变** |
| coincoin-proxy | coincoin 主站（GitHub `hezhaoqian1/coincoin-proxy` master） | 无状态 | clawfather.up.railway.app、coincoin.ai | coincoin.ai 要重新 TXT 验证 |
| 中转站MySQL | coincoin 库（约 6.5 GB，请求日志占 5.3 GB） | 预拷贝 + 切换时补增量 | 内网 | 不变 |
| 中转站Redis | coincoin 缓存（几十个临时键） | 不迁，新库从空开始 | 内网 | 不变 |
| 通联专属 | 通联站（同 coincoin-proxy 代码） | 无状态 | datayes.up.railway.app、datayes.coincoin.ai | datayes.coincoin.ai 要重新 TXT 验证 |
| 通联MySQL | 通联库（约 0.75 GB） | 预拷贝 + 切换时补增量 | 内网 | 不变 |
| 通联Redis | 通联缓存 | 不迁 | 内网 | 不变 |

不变的：**静态出口 IP**（新加坡共享池 208.77.246.240/.241/.242，10-29 实测新旧一致，上游白名单不用改；每次仍要核对）、所有环境变量、副本数、资源上限、区域（asia-southeast1-eqsg3a）。

> 旧账号里还有别的项目（Kredo 系列、new-api-railway、relay-audit、deepseek-lab 等），不在本流程内，要迁的话单独确认。`00_inventory.py` 会把旧账号所有项目列出来提醒。

---

## 2. 提前准备（迁移前 1～3 天）

**新账号**
- [ ] PRO 计划（静态出口 IP、多副本要 PRO），已绑卡，**没有设置 usage hard limit**
- [ ] 账户级 API token（Account Settings → Tokens），切换时设为 `NEW_RAILWAY_TOKEN`
- [ ] 本机 `railway ssh` 能用（第一次用新 token 时 CLI 会注册本机 SSH key）
- [ ] 新账号 GitHub 授权能访问 `hezhaoqian1/BirdSub2api`、`hezhaoqian1/coincoin-proxy`（两个都是公开仓库，能部署；但**推代码自动部署**需要 Railway GitHub App 有权限，迁完要验证）

**Cloudflare**
- [ ] **coincoin.ai** 区域的 API token（权限 Zone → DNS → Edit），设为 `CF_TOKEN_COINCOIN`。有了它脚本会自动加 TXT，coincoin.ai 只停 4 分钟左右；没有就得有人在线手动加。
- birdbirdapi.com 的 token 已经在 caddy-long 的 `CF_API_TOKEN` 变量里，脚本自动用。

**本机**
- Python 3.10+、Railway CLI；旧账号用 CLI 登录或设 `OLD_RAILWAY_TOKEN`
- 复制 `migration.example.json` 为 `work/migration.json`，填旧项目 ID/环境 ID、新工作区 ID（`work/` 已 gitignore，里面会有带密钥的导出文件，**不要提交**）
  - 本机的 `work/migration.json` 已经按"下个月从当前项目迁出"填好（old = 当前的 fearless-luck），只差 `new.workspace_id`
  - 脚本先只读跑一遍 `00_inventory.py` 确认能连上，再往下走

**沟通**
- 选低峰时段（建议北京时间 04:00～06:00）
- 提前告诉长任务客户：迁移后端口会变，新端口切换后发出

---

## 3. 操作步骤

所有脚本在 `deploy/railway-migration/scripts/`，从仓库根目录运行，例如 `python -X utf8 deploy/railway-migration/scripts/00_inventory.py`。
脚本输出都带时间戳；每一步看到"成功标志"再做下一步。

### A. 盘点（不影响线上）
```
python -X utf8 scripts/00_inventory.py
```
- 导出旧环境完整配置（含解密变量）到 `work/old_env_SECRET.json`
- 记录：服务、副本、卷大小、域名、TCP 代理、出口 IP、Postgres 非默认参数、MySQL `persisted_variables`、Redis 运行参数 → `work/inventory.json`
- **成功标志**：打印出每个服务的摘要，最后列出旧账号所有项目

### B. 搭建新项目 + 启动同步（不影响线上）
```
python -X utf8 scripts/01_build_new.py
python -X utf8 scripts/02_start_sync.py
```
`01_build_new.py`：
1. 新工作区建同名项目、同名空服务、同大小的卷（**卷大小在首次部署前就设好**，事后扩容会触发重启）
2. 旧 Postgres：pg_hba 加一行复制规则 + reload（不重启）、建物理复制槽 `mig_newacct`
3. 一次性提交新环境配置：变量全量照搬；其他区域显式置空（新服务默认带 us-west2）；**三个应用的启动命令改成 `tail -f /dev/null`（休眠）**；Postgres 用引导脚本（空卷时 `pg_basebackup` 拉旧库，之后作为从库启动）
4. 给原来有静态出口 IP 的服务开出口 IP，并和旧的对比
5. 给有域名的应用先建临时域名（切换时改名为正式域名）
6. `railway up` 上传 caddy-long

`02_start_sync.py`：Redis `REPLICAOF` 旧库；两个 MySQL 后台预拷贝；重放旧 MySQL 的 `SET PERSIST` 参数（如 `max_connections=500`）

为什么应用要休眠：网关会刷新上游 OAuth token、跑渠道监控等后台任务，两边同时跑会互相把 token 刷掉；而且 Postgres 还是只读从库，网关起来会报错。

### C. 检查同步
```
python -X utf8 scripts/03_status.py
```
**成功标志**：
- Postgres：`in_recovery=t`、`wal receiver=streaming`、回放延迟 < 1 秒
- Redis：`role:slave`、`master_link_status:up`
- MySQL 预拷贝：`/tmp/precopy.done` 存在（中转站库请求日志很大，**不用等它**，切换时会处理）
- 三个应用最新部署 SUCCESS（只是构建好，没在跑）

### D. 切换（会短暂停服，按顺序做）

**D1. 网关**（约 2 分钟）
```
python -X utf8 scripts/10_cutover_gateway.py
```
流程：停旧网关（核实实例已退出）→ 等旧库应用连接归零 → 记录旧库最后 LSN → 等新库回放追平 → `pg_promote()` → 新 Redis `REPLICAOF NO ONE` → 新网关恢复正式启动命令和健康检查（构建命中缓存，约 40 秒）→ 删旧域名、新服务认领 `birdapi.up.railway.app`（约 7 秒）→ `/health` 200。
**回滚**：新网关起不来时脚本自动用旧镜像重新部署旧网关；旧库全程没动。

**D2. 长任务入口**
```
python -X utf8 scripts/12_long_entry.py
```
把 `api.birdbirdapi.com` 的 CNAME 指到新 TCP 代理，打印新地址 `https://api.birdbirdapi.com:<新端口>` → **发给长任务客户**。

**D3. 通联、D4. coincoin**（各约 2 分钟 + 域名验证）
```
python -X utf8 scripts/11_cutover_app.py tl
python -X utf8 scripts/11_cutover_app.py zz
```
顺序（已按 10-29 的教训调整）：
1. 停旧应用，**核实实例真的退出**；`deploymentStop` 不生效就 `deploymentRemove`；断开旧服务的 GitHub
2. **先转自定义域名**：删旧的 → 新服务创建 → 拿到新验证值 → 有 `CF_TOKEN_COINCOIN` 就自动加 TXT，否则打印出来让人加。Railway 大约 3 分钟验证完，这段时间和下面的数据同步重叠
3. 如果预拷贝还没跑完，先停掉它，否则它稍后可能用旧快照覆盖新数据
4. 补关键数据（应用停着，数据一致）：小表全量重拷，账单流水、图片任务按时间窗口补
5. 启动新应用（恢复原启动命令）
6. 转 `*.up.railway.app` 域名
7. 后台按天、不锁表地补请求日志（`--skip-add-locks`，见第 5 节第 5 条）

### E. 全面验证
```
python -X utf8 scripts/20_verify.py            # 入口、副本、配置/参数对比
python -X utf8 scripts/20_verify.py --models   # 再加：每个活跃分组建临时 key 测模型，测完自动删 key
```
清单见第 6 节。

### F. 收尾
```
python -X utf8 scripts/30_cleanup.py
```
- 旧 Postgres：删复制槽 `mig_newacct`、撤 pg_hba 复制规则（不删会让旧库 WAL 越积越多）
- 旧应用：确认全部 EXITED/REMOVED，并已断开 GitHub
- 新 Postgres：清掉残留的 `primary_conninfo`（里面有旧库密码，reload 即可）；启动命令、`MIG_*` 变量只暂存（`skipDeploys`），等下次正常重启生效。`MIG_BOOT_B64` 留到下次部署后再删（当前进程的重启还依赖它）
- 旧数据库**保留几天**当备份，确认无误再在旧账号删除；之后停用旧账号

---

## 4. 关键技术点

**Postgres：物理流复制，不用 pg_dump**
- `wal_level=replica` 就够（Railway 默认），不用重启旧库
- 旧库 pg_hba 原有的 `host all all all scram-sha-256` **不匹配复制连接**，要加 `host replication postgres all scram-sha-256` 再 `pg_reload_conf()`
- 新库启动命令用 `scripts/pg_standby_boot.sh`（base64 放在变量 `MIG_BOOT_B64` 里）：卷为空时 `pg_basebackup -R -X stream -S mig_newacct` 拉旧库（经旧库 TCP 代理），完成后交给 Railway 镜像自带的 `wrapper.sh` 正常启动
- 切换时等回放追平旧库最后 LSN 再 `pg_promote()`：数据一字节不差，主键序列也不用管

**Redis**：新库 `CONFIG SET masterauth` + `REPLICAOF <旧库TCP代理>`，切换时 `REPLICAOF NO ONE`。没有 TCP 代理的 Redis（coincoin 两个）只有临时缓存，不迁。

**MySQL**：旧库启动参数带 `--disable-log-bin`，没法做主从，所以先预拷贝，切换时补增量。主键是 UUID 字符串，只能按 `created_at`/`updated_at` 时间窗口补。**建议开 binlog**，下次就能用主从复制（见第 8 节）。

**域名**
- `*.up.railway.app`：旧服务删掉后约 3 秒新账号就能认领（`serviceDomainUpdate` 改名），客户端地址不变
- 自定义域名（Cloudflare 橙色云朵代理）：CNAME 不改也能用，但 Railway 要重新验证归属，`_railway-verify` 的 **TXT 值每个新项目都不同**，而且只有在新项目创建域名那一刻才生成。TXT 生效后 Railway 约 3 分钟验证完。旧的 TXT 可以留着。

**静态出口 IP**：新加坡是共享 IP 池，10-29 新旧完全一致。每次用 `20_verify.py` 核对，变了就要通知上游改白名单。

**应用启动**：改配置提交会触发一次构建，但全部命中缓存（网关约 36 秒）。`deploymentRedeploy(usePreviousImageTag)` 沿用旧部署的**配置快照**，不能用来改启动命令。

---

## 5. 10-29 踩过的坑（都已修进脚本）

| # | 现象 | 原因 | 对策 |
|---|---|---|---|
| 1 | 配置提交失败："Services with volumes cannot be deployed to multiple regions" | 新服务默认带 us-west2 区域，补丁是合并的 | `multiRegionConfig` 里其他区域显式置 `null` |
| 2 | pg_basebackup 报 Permission denied | 在旧 PGDATA 里放了 root 属主的 pg_hba 备份文件 | 备份文件放到 PGDATA 外面 |
| 3 | 新 Postgres 扩卷时自己重启，网关断库约 15 秒，16 个请求没记上用量 | 通过 environmentPatch 改卷大小**即使 skipDeploys 也会重启** | 建卷时（首次部署前）就设成旧卷大小；之后扩容放低峰 |
| 4 | 旧 coincoin-proxy `deploymentStop` 返回成功但一直在跑 | Railway 行为不稳定 | 停完核实实例状态，不行就 `deploymentRemove` |
| 5 | 后台补请求日志导致 coincoin 连接池耗尽、API 报 500，持续约 20 分钟 | mysqldump 默认带 `LOCK TABLES ... WRITE`，导入时整表写锁；请求日志表有外键，连 `UPDATE api_keys` 也被卡住 | 线上补数据一律 `--skip-add-locks`，按天分批 |
| 6 | 新 MySQL `max_connections` 是 151（旧的 500） | 旧库用 `SET PERSIST` 设的，逻辑导出带不过去 | 盘点 `performance_schema.persisted_variables`，新库重放 |
| 7 | datayes.coincoin.ai / coincoin.ai 停了 15～19 分钟 | 新项目要新的 TXT 验证，等人工添加 | 提前要 `CF_TOKEN_COINCOIN`，脚本自动加；先转域名再同步数据，让验证时间和同步重叠 |
| 8 | 中转站库预拷贝 40 分钟才导了一半 | 请求日志 5.3 GB，单线程导入 | 切换不等预拷贝；关键表全量 + 窗口补，日志后台补 |
| 9 | 预拷贝没跑完就切换有覆盖风险 | 预拷贝按表名字母序 DROP/重建，会用旧快照覆盖之后的表 | 切换前先停掉预拷贝 |
| 10 | clawfather 多停了 20 分钟 | 增量里请求日志窗口补得慢，挡住了启动；我的脚本还误判了部署状态 | 请求日志窗口挪到应用启动之后在后台补 |
| 11 | Windows 下 Python 调 `railway` 找不到命令；远端命令引号转义出错 | 要用 `railway.cmd`；shell 引号嵌套 | `lib.py` 自动找 CLI；远端脚本统一 base64 传过去 |
| 12 | 旧账号 GraphQL 报 Not Authorized | CLI 的 OAuth token 过期；`serviceDomainAvailable` 只有新账号 token 能查 | 先 `railway whoami` 刷新，或直接用 `OLD_RAILWAY_TOKEN` |

---

## 6. 验证清单

- [ ] 所有入口 200：birdapi `/health`、coincoin.ai、clawfather、datayes.coincoin.ai、datayes.up.railway.app；长任务入口不带 key 返回 401
- [ ] 副本：网关 2 个 RUNNING，其余各 1 个
- [ ] 配置对比无意外差异（资源上限、启动命令、健康检查、优雅下线时间、区域、变量名集合、卷大小）
- [ ] Postgres 非默认参数一致；MySQL `max_connections` 等持久化参数一致；Redis 参数一致
- [ ] 静态出口 IP 和旧的一致
- [ ] 网关按分组测模型：每个活跃分组的主力模型都 200，测试请求有记账（10-29：17 组 16 组正常，剩下 1 组的 404 是某个上游账号不支持该模型，跟迁移无关）
- [ ] 网关近 15 分钟 5xx 没有异常升高，`usage_logs` 持续写入
- [ ] coincoin 后台概览能加载（`/admin/metrics/summary` 200），数据库没有长时间等锁的连接
- [ ] coincoin 关键表行数一致：users、credit_allocations、credit_balances、payment_orders、api_keys（billing_ledger 新库只会多）
- [ ] 旧应用全部 EXITED/REMOVED，并已断开 GitHub
- [ ] 推一次代码，确认新账号能自动部署

---

## 7. 10-29 实际时间线（北京时间）

| 时间 | 事件 |
|---|---|
| 10:09 | 新项目建好、配置提交（第一次因 us-west2 失败，第二次成功） |
| 10:16～10:33 | Postgres 基础备份（第一次因坑 2 失败重来），之后流复制，延迟 0.2 秒 |
| 10:44:02～10:46:02 | **网关切换**，停服 2 分钟，LSN 完全一致 |
| 10:47 | 长任务入口切到新端口 44353 |
| 10:49～10:50 | **通联切换**，增量 10 秒，45 张表行数一致；datayes.coincoin.ai 等 TXT 到 11:05 |
| 10:59 | 扩卷导致新 Postgres 重启（坑 3） |
| 11:06～11:30 | **coincoin 切换**：coincoin.ai 等 TXT 到约 11:25，clawfather 11:30 恢复（坑 7、10） |
| 11:31～11:56 | 后台补日志锁表，coincoin 后台和部分 API 报错（坑 5），停掉补日志后恢复 |
| 11:59 | 旧 coincoin-proxy 用 deploymentRemove 关掉（坑 4） |
| 12:05 | 网关 17 组模型测试，16 组正常 |

---

## 8. 下次迁移前要做的（做了能明显缩短停服）

- [ ] **新 MySQL 开 binlog**：去掉启动命令里的 `--disable-log-bin`，加 `--server-id=<唯一值> --binlog-expire-logs-seconds=259200`。要重启一次 MySQL，放低峰。开了之后下次可以做 MySQL 主从，切换零增量
- [ ] 拿到 `CF_TOKEN_COINCOIN`
- [ ] 中转站库 `coincoin_request_logs` 定保留期或归档（现在 5.3 GB，是 MySQL 迁移慢的主要原因）
- [ ] 新 Postgres 开每日自动备份（旧账号也没开）
- [ ] 下次迁移时，"旧账号"就是这次的新账号：把 `work/migration.json` 的 old 改成当前项目，new 改成下一个工作区
