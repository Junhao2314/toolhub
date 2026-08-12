import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { Languages } from 'lucide-react'

export type Lang = 'en' | 'zh'

// English source strings are dictionary keys; missing entries fall back to English.
const zh: Record<string, string> = {
  // Shell and shared UI
  'Overview': '概览',
  'Skills': '技能',
  'Marketplace': '市场',
  'MCP': 'MCP',
  'Profiles': '配置集',
  'Targets': '目标',
  'Operations': '操作记录',
  'Settings': '设置',
  'Account': '账户',
  'Opening ToolHub': '正在打开 ToolHub',
  'Open navigation': '打开导航',
  'Close navigation': '关闭导航',
  'Sign out': '退出登录',
  'This account is using a temporary password. Change it from Account.': '此账户仍使用初始密码，请前往「账户」修改。',
  'Loading': '加载中',
  'Retry': '重试',
  'Close': '关闭',
  'Cancel': '取消',
  'Refresh': '刷新',
  'Search': '搜索',
  'Actions': '操作',
  'Name': '名称',
  'Description': '描述',
  'Source': '来源',
  'Revision': '修订版本',
  'Updated': '更新时间',
  'Created': '创建时间',
  'Expires': '过期时间',
  'State': '状态',
  'Reason': '原因',
  'Kind': '类型',
  'Scope': '作用域',
  'Hash': '哈希',
  'Save': '保存',
  'Edit': '编辑',
  'Delete': '删除',
  'Add': '添加',
  'Remove': '移除',
  'Import': '导入',
  'Importing': '正在导入',
  'Select': '选择',
  'Started': '开始时间',
  'Finished': '完成时间',

  // Status values
  'online': '在线',
  'unavailable': '不可用',
  'queued': '已排队',
  'running': '执行中',
  'succeeded': '成功',
  'partial': '部分完成',
  'failed': '失败',
  'cancelled': '已取消',
  'healthy': '健康',
  'drifted': '已漂移',
  'repairing': '修复中',
  'blocked': '已阻止',
  'suspended': '已暂停重试',
  'paused': '已暂停',
  'protected': '受保护',
  'managed': '受管理',
  'unmanaged': '未管理',
  'ready': '就绪',
  'active': '运行中',
  'unknown': '未知',
  'local': '本机',
  'salt': 'Salt',
  'zip': 'ZIP',
  'git': 'Git',

  // Authentication and account
  'Sign in': '登录',
  'Signing in': '正在登录',
  'Username': '用户名',
  'Password': '密码',
  'Credentials and active sessions': '凭据与活动会话',
  'Current password': '当前密码',
  'New password': '新密码',
  'Confirm password': '确认密码',
  'Update username': '更新用户名',
  'Update password': '更新密码',
  'Passwords do not match': '两次输入的密码不一致',

  // Overview
  'Loading fleet status': '正在加载目标状态',
  'Overview unavailable': '概览不可用',
  'Desired state and target health': '期望状态与目标健康度',
  'Needs attention': '需要关注',
  'MCP servers': 'MCP 服务',
  'Target health': '目标健康度',
  'All desired targets are healthy': '所有期望目标均健康',
  'Target': '目标',
  'Runtime': '运行时',
  'Desired revision': '期望修订版本',
  'Recent operations': '最近操作',
  'History': '历史',
  'No operations': '暂无操作记录',

  // Skills
  'Immutable Skill library': '不可变 Skill 资源库',
  'Skill imported': 'Skill 已导入',
  'Upload ZIP': '上传 ZIP',
  'Import Git': '从 Git 导入',
  'Search skills': '搜索 Skills',
  'Check now': '立即检查',
  'Update check queued': '更新检查已排队',
  'Loading skills': '正在加载 Skills',
  'No Skills in Library': '资源库中暂无 Skills',
  'Skill': 'Skill',
  'Commit': 'Commit',
  'Artifact': '制品',
  'Import queued': '导入已排队',
  'Import Git Skill': '从 Git 导入 Skill',
  'HTTPS repository': 'HTTPS 仓库',
  'Subdirectory': '子目录',
  'Commit or ref': 'Commit 或 ref',
  'Queue import': '排队导入',

  // Marketplace
  'All sources': '全部来源',
  'Search marketplace': '搜索市场',
  'Searching': '正在搜索',
  'No marketplace results': '没有市场搜索结果',
  'Author': '作者',
  'Downloads': '下载量',
  'Stars': 'Stars',
  'Open source': '打开来源',
  'Version': '版本',
  'URL': 'URL',

  // MCP
  'Server library and write-only secrets': 'MCP 服务资源库与只写密钥',
  'Add server': '添加服务',
  'Search MCP servers': '搜索 MCP 服务',
  'No MCP servers': '暂无 MCP 服务',
  'Server': '服务',
  'Transport': '传输方式',
  'Endpoint': '端点',
  'Secrets': '密钥',
  'Secret keys must be unique': '密钥名称不能重复',
  'Add MCP server': '添加 MCP 服务',
  'Command': '命令',
  'Arguments': '参数',
  'Environment secrets': '环境变量密钥',
  'Header secrets': 'Header 密钥',
  'Key': '键名',
  'Value': '值',
  'Unchanged': '保持不变',

  // Profiles
  'Unified Skill and MCP membership': '统一的 Skill 与 MCP 成员关系',
  'New Profile': '新建配置集',
  'No Profiles': '暂无配置集',
  'Profile': '配置集',
  'Preflight and Apply': '预检并应用',
  'Apply queued': '应用已排队',
  'Apply Profile': '应用配置集',
  'excluded': '项已排除',
  'Confirm Apply': '确认应用',
  'Run preflight': '执行预检',

  // Targets and relay
  'Node refresh queued': '节点刷新已排队',
  'Node refresh running': '节点刷新进行中',
  'Node refresh completed': '节点刷新已完成',
  'Node refresh failed': '节点刷新失败',
  'Node refresh cancelled': '节点刷新已取消',
  'Runtime inventory and pinned desired snapshots': '运行时清单与已固定的期望快照',
  'Refresh nodes': '刷新节点',
  'No Targets': '暂无目标',
  'Scan': '扫描',
  'Scan queued': '扫描已排队',
  'Edit desired membership': '编辑期望成员',
  'Start': '启动',
  'Stop': '停止',
  'Restart': '重启',
  'Health check': '健康检查',
  'Relay start queued': 'Relay 启动已排队',
  'Relay stop queued': 'Relay 停止已排队',
  'Relay restart queued': 'Relay 重启已排队',
  'Health check queued': '健康检查已排队',
  'Port': '端口',
  'Enabled': '已启用',
  'Disabled': '未启用',
  'Retry count': '重试次数',
  'Next retry': '下次重试',
  'Retry state': '重试状态',
  'Last full check': '上次完整检查',
  'Runtime contract': '运行时契约',
  'verified': '已验证',
  'incompatible': '不兼容',
  'Desired MCP health': '期望 MCP 健康状态',
  'No desired MCP members': '没有期望 MCP 成员',
  'Capabilities': '能力',
  'Checked': '检查时间',
  'Tools': '工具',
  'Resources': '资源',
  'Templates': '资源模板',
  'Prompts': '提示词',
  'Not checked': '尚未检查',
  'Snapshot source': '快照来源',
  'Last scan': '上次扫描',
  'Last reconcile': '上次调和',
  'Inventory': '清单',
  'No inventory snapshot': '暂无清单快照',
  'Member': '成员',
  'Backups': '备份',
  'No backups': '暂无备份',
  'Restore': '恢复',
  'Edit queued': '编辑已排队',
  'Restore queued': '恢复已排队',
  'Edit target': '编辑目标',
  'Apply edit': '应用编辑',
  'Backup': '备份',
  'Target revision': '目标修订版本',
  'Import Skill': '导入 Skill',
  'Skill import queued': 'Skill 导入已排队',
  'Import MCP from runtime': '从运行时导入 MCP',
  'No importable MCP servers': '没有可导入的 MCP 服务',
  'Read and encrypt the selected secret values once': '确认一次性读取并加密所选服务的密钥值',
  'Import selected': '导入所选服务',
  'MCP import queued': 'MCP 导入已排队',
  'Edit node username': '编辑节点用户名',
  'Node username override': '节点用户名覆盖值',
  'Node username override saved': '节点用户名覆盖值已保存',
  'Node username override cleared': '节点用户名覆盖值已清除',
  'Salt node is no longer available': 'Salt 节点已不可用',

  // Operations
  'Apply, reconcile, import, restore, and relay history': '应用、调和、导入、恢复与 Relay 历史',
  'Cancellation requested': '已请求取消',
  'Retry failed targets': '重试失败目标',
  'Retry queued': '重试已排队',
  'View details': '查看详情',
  'Operation details': '操作详情',
  'Attempt': '尝试次数',
  'Bridge operation': 'Bridge 操作',
  'Salt JID': 'Salt JID',

  // Settings
  'Settings saved': '设置已保存',
  'Global runtime and update policy': '全局运行时与更新策略',
  'Managed runtime': '受管理运行时',
  'Managed username': '受管理用户名',
  'Relay port': 'Relay 端口',
  'Library updates': '资源库更新',
  'Cron schedule': 'Cron 计划',
  'Timezone': '时区',
  'Save settings': '保存设置',
}

interface I18nValue {
  lang: Lang
  setLang: (lang: Lang) => void
  t: (key: string, vars?: Record<string, string | number>) => string
}

const I18nContext = createContext<I18nValue>({ lang: 'en', setLang: () => {}, t: (key) => key })

const STORAGE_KEY = 'toolhub.lang'

function initialLang(): Lang {
  const stored = localStorage.getItem(STORAGE_KEY)
  if (stored === 'en' || stored === 'zh') return stored
  return navigator.language.toLowerCase().startsWith('zh') ? 'zh' : 'en'
}

export function LanguageProvider({ children }: { children: ReactNode }) {
  const [lang, setLang] = useState<Lang>(initialLang)
  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, lang)
    document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en'
  }, [lang])
  const t = (key: string, vars?: Record<string, string | number>) => {
    let text = lang === 'zh' ? zh[key] ?? key : key
    if (vars) for (const [name, value] of Object.entries(vars)) text = text.replaceAll(`{${name}}`, String(value))
    return text
  }
  return <I18nContext.Provider value={{ lang, setLang, t }}>{children}</I18nContext.Provider>
}

export function useI18n() {
  return useContext(I18nContext)
}

export function LanguageToggle() {
  const { lang, setLang } = useI18n()
  const next = lang === 'zh' ? 'en' : 'zh'
  return <button className="lang-toggle" onClick={() => setLang(next)} title={lang === 'zh' ? 'Switch to English' : '切换为中文'}>
    <Languages size={15} /><span>{lang === 'zh' ? 'EN' : '中文'}</span>
  </button>
}
