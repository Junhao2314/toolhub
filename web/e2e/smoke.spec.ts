import { expect, test, type Page } from '@playwright/test'

async function expectMobileNavigationClosed(page: Page, projectName: string) {
  if (projectName !== 'mobile') return
  await expect(page.locator('.nav-scrim')).toHaveCount(0)
  await expect.poll(async () => {
    const box = await page.locator('.sidebar').boundingBox()
    return box ? Math.round(box.x + box.width) : 0
  }).toBeLessThanOrEqual(0)
}

test('login and core navigation render without overlap', async ({ page }, testInfo) => {
  const email = process.env.TOOLHUB_E2E_EMAIL
  const username = process.env.TOOLHUB_E2E_USERNAME ?? 'admin'
  const localNodeName = process.env.TOOLHUB_E2E_LOCAL_NODE_NAME ?? 'project-host'
  const password = process.env.TOOLHUB_E2E_PASSWORD
  if (!email || !password) throw new Error('TOOLHUB_E2E_EMAIL and TOOLHUB_E2E_PASSWORD are required')
  const consoleErrors: string[] = []
  page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(message.text()) })

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'ToolHub' })).toBeVisible()
  await page.getByLabel('Username or email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  await expect(page.getByText('Tailnet control plane')).toBeVisible()
  if (process.env.TOOLHUB_E2E_EXPECT_PASSWORD_CHANGE === 'true') {
    await expect(page.getByText('This account is using a temporary password.')).toBeVisible()
  }

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Account', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Account' })).toBeVisible()
  await expect(page.getByLabel('Username')).toHaveValue(username)
  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Overview', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Skills', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Skills' })).toBeVisible()
  await expect(page.getByLabel('Skill provenance')).toBeVisible()
  await expect(page.locator('main')).toHaveCSS('overflow-x', 'visible')

  const viewport = page.viewportSize()!
  const header = await page.locator('.page-header').boundingBox()
  const toolbar = await page.locator('.toolbar').boundingBox()
  expect(header).not.toBeNull()
  expect(toolbar).not.toBeNull()
  expect(header!.y + header!.height).toBeLessThanOrEqual(toolbar!.y + 1)
  expect(header!.x).toBeGreaterThanOrEqual(0)
  expect(header!.x + header!.width).toBeLessThanOrEqual(viewport.width + 1)

  await page.screenshot({ path: `../test-results/${testInfo.project.name}-skills.png`, fullPage: true })

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'MCP', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'MCP', exact: true })).toBeVisible()
  await expect(page.getByLabel('MCP provenance')).toBeVisible()
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-mcp.png`, fullPage: true })

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('navigation').getByRole('button', { name: 'Profiles', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Profiles' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add Profile' })).toBeVisible()
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-profiles.png`, fullPage: true })

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Runtime View', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Runtime View' })).toBeVisible()
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-runtime-view.png`, fullPage: true })

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Nodes', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Nodes' })).toBeVisible()
  await expect(page.getByText('Project host', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Configure pinned SSH fallback' }).first().click()
  await expect(page.getByRole('heading', { name: /SSH fallback/ })).toBeVisible()
  await expect(page.getByLabel('Pinned known_hosts line')).toBeVisible()
  await expect(page.getByLabel('Private key')).toBeVisible()
  await page.getByRole('button', { name: 'Close', exact: true }).click()
  await page.getByRole('button', { name: 'Enroll project host' }).click()
  await expect(page.getByRole('heading', { name: /Enroll project host/ })).toBeVisible()
  await expect(page.getByLabel('Node name')).toHaveValue(localNodeName)
  await page.getByRole('button', { name: 'Close', exact: true }).click()
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-nodes.png`, fullPage: true })
  await page.getByRole('button', { name: 'Sign out' }).click()
  await page.getByLabel('Username or email').fill(username)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  expect(consoleErrors).toEqual([])
})

test('viewer can inspect Profiles and Runtime View without mutation controls', async ({ page }, testInfo) => {
  const node = { id: '11111111-1111-4111-8111-111111111111', name: 'viewer-node', status: 'online', isLocal: true, runtimeKinds: ['codex', 'hermes'], activations: [{ runtime: 'codex', profileId: '22222222-2222-4222-8222-222222222222', profileName: 'Research', state: 'active' }] }
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') {
      await route.fulfill({ json: { authenticated: true, csrfToken: 'viewer-csrf', user: { id: '33333333-3333-4333-8333-333333333333', username: 'viewer', email: 'viewer@toolhub.local', displayName: 'Viewer', roles: ['viewer'] } } })
      return
    }
    if (path === '/api/v1/profiles') {
      await route.fulfill({ json: { items: [{ id: '22222222-2222-4222-8222-222222222222', name: 'Research', description: 'Read-only fixture', mcpServerCount: 1, skillCount: 1, activationCount: 1 }] } })
      return
    }
    if (path === '/api/v1/nodes') {
      await route.fulfill({ json: { items: [node] } })
      return
    }
    if (path === '/api/v1/skills' || path === '/api/v1/mcp/servers') {
      await route.fulfill({ json: { items: [] } })
      return
    }
    if (path === `/api/v1/targets/${node.id}/codex`) {
      await route.fulfill({ json: { node, runtime: 'codex', capabilities: { skills: true, mcp: true, mcpNote: '' }, activation: { profileId: '22222222-2222-4222-8222-222222222222', profileName: 'Research', previousProfileId: '', previousProfileName: '', state: 'active', lastError: '', skipped: [], activatedAt: '2026-07-28T00:00:00Z', activatedBy: 'admin' }, mcp: { mcpmProfile: 'toolhub-codex', deploymentId: '44444444-4444-4444-8444-444444444444', state: 'in_sync', servers: [] }, skills: [], drift: { mcp: 0, skills: 0 } } })
      return
    }
    if (path === `/api/v1/targets/${node.id}/hermes`) {
      await route.fulfill({ json: {
        node,
        runtime: 'hermes',
        capabilities: { skills: true, mcp: true, readOnly: true, mcpNote: '' },
        activation: null,
        mcp: { mcpmProfile: '', deploymentId: '', state: 'observed', servers: [{ id: '55555555-5555-4555-8555-555555555555', name: 'hermes-browser', runtimeName: 'hermes-browser', transport: 'stdio', endpoint: 'hermes-browser', enabled: true, source: 'hermes', originName: '', missing: false, drift: false, observedGeneration: 2, sourceChanged: true, readOnly: true }] },
        skills: [{ discoveryId: '66666666-6666-4666-8666-666666666666', skillId: '', name: 'Hermes Notes', slug: 'hermes-notes', desiredEnabled: false, actualEnabled: false, state: 'observed', desiredVersionId: '', actualVersionId: '', sha256: 'aaaaaaaaaaaa', lastError: '', path: '/opt/hermes/skills/notes', importStatus: 'available', sourceChanged: false, readOnly: true }],
        drift: { mcp: 0, skills: 0 },
      } })
      return
    }
    await route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/profiles')
  await expect(page.getByRole('heading', { name: 'Profiles' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add Profile' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Activate Profile' })).toHaveCount(0)
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-viewer-profiles.png`, fullPage: true })

  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
  }
  await page.getByRole('button', { name: 'Runtime View', exact: true }).click()
  await expectMobileNavigationClosed(page, testInfo.project.name)
  await expect(page.getByRole('heading', { name: 'Runtime View' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Active Profile' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Retry activation' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Deactivate Profile' })).toHaveCount(0)
  await page.getByRole('combobox', { name: 'Runtime', exact: true }).selectOption('hermes')
  await expect(page.getByRole('heading', { name: 'Read-only runtime source' })).toBeVisible()
  await expect(page.getByText('Observation and explicit import only')).toBeVisible()
  await expect(page.locator('tbody tr').filter({ hasText: 'hermes-browser' })).toHaveCount(1)
  await expect(page.getByText('Hermes Notes', { exact: true })).toBeVisible()
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-viewer-runtime-view.png`, fullPage: true })
})

test('admin uses explicit Hermes imports and confirms MCP secret changes', async ({ page }, testInfo) => {
  const nodeID = '11111111-1111-4111-8111-111111111111'
  const skillDiscoveryID = '22222222-2222-4222-8222-222222222222'
  const mcpDiscoveryID = '33333333-3333-4333-8333-333333333333'
  const serverID = '44444444-4444-4444-8444-444444444444'
  const skillSHA = 'a'.repeat(64)
  const node = { id: nodeID, name: 'mixed-runtime-node', status: 'online', isLocal: true, runtimeKinds: ['codex', 'hermes'], activations: [] }
  const librarySkill = { id: '55555555-5555-4555-8555-555555555555', name: 'Imported formatter', slug: 'imported-formatter', description: 'Reviewed Hermes snapshot', reviewStatus: 'approved', sourceKind: 'hermes-import', sha256: skillSHA, deploymentCount: 0, protected: false }
  const discoveries = [
    { id: skillDiscoveryID, kind: 'skill', nodeName: node.name, runtime: 'hermes', name: 'Hermes Formatter', path: '/opt/hermes/skills/formatter', sha256: skillSHA, managed: false, protected: false, missing: false, drift: false, status: 'available', lastError: '', controlMode: 'read_only_source', sourceChanged: false, importStatus: 'available' },
    { id: '66666666-6666-4666-8666-666666666666', kind: 'skill', nodeName: node.name, runtime: 'codex', name: 'Codex Local Skill', path: '/opt/codex/skills/local', sha256: 'b'.repeat(64), managed: false, protected: false, missing: false, drift: false, status: 'unmanaged', lastError: '', controlMode: 'managed_target', sourceChanged: false, importStatus: 'not_applicable' },
    { id: mcpDiscoveryID, kind: 'mcp', nodeName: node.name, runtime: 'hermes', name: 'hermes-search', missing: false, status: 'available', lastError: '', serverId: '', controlMode: 'read_only_source', sourceChanged: false, importStatus: 'available', observedGeneration: 7, envKeys: ['SEARCH_TOKEN'], headerKeys: ['Authorization'] },
  ]
  const server = { id: serverID, name: 'imported-hermes-search', runtimeName: 'hermes-search', transport: 'stdio', command: 'hermes-search', args: ['--stdio'], url: '', enabled: true, healthStatus: 'unknown', source: 'hermes-import', origin: { importSourceName: 'Hermes', serverName: 'hermes-search' }, authority: 'toolhub', credentialMode: 'encrypted', envKeys: ['SEARCH_TOKEN'], headerKeys: ['Authorization'], bindingCount: 1, hasDrift: false }
  let skillImportBody: Record<string, unknown> | null = null
  const mcpImportBodies: Record<string, unknown>[] = []
  const mcpPatchBodies: Record<string, unknown>[] = []
  const expectedConflictErrors: string[] = []
  const consoleErrors: string[] = []
  page.on('console', (message) => {
    if (message.type() !== 'error') return
    if (message.text().includes('status of 409 (Conflict)')) expectedConflictErrors.push(message.text())
    else consoleErrors.push(message.text())
  })

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') {
      await route.fulfill({ json: { authenticated: true, csrfToken: 'admin-csrf', user: { id: '77777777-7777-4777-8777-777777777777', username: 'admin', email: 'admin@toolhub.local', displayName: 'Admin', roles: ['admin'] } } })
      return
    }
    if (path === '/api/v1/skills') {
      await route.fulfill({ json: { items: [librarySkill] } })
      return
    }
    if (path === '/api/v1/deployments') {
      await route.fulfill({ json: { items: [] } })
      return
    }
    if (path === '/api/v1/discoveries') {
      await route.fulfill({ json: { items: discoveries } })
      return
    }
    if (path === `/api/v1/discoveries/${skillDiscoveryID}/import-skill`) {
      skillImportBody = request.postDataJSON()
      await route.fulfill({ status: 202, json: { id: '88888888-8888-4888-8888-888888888888', kind: 'skill_snapshot_import', status: 'pending' } })
      return
    }
    if (path === `/api/v1/discoveries/${mcpDiscoveryID}/import-mcp`) {
      const body = request.postDataJSON() as Record<string, unknown>
      mcpImportBodies.push(body)
      if (!body.confirmSecrets) {
        await route.fulfill({ status: 409, json: { error: { code: 'secret_confirmation_required', message: 'Confirm named Hermes secrets', envKeys: ['SEARCH_TOKEN'], headerKeys: ['Authorization'], targets: [] } } })
      } else {
        await route.fulfill({ status: 202, json: { id: '99999999-9999-4999-8999-999999999999', kind: 'inventory_scan', status: 'pending' } })
      }
      return
    }
    if (path === '/api/v1/nodes') {
      await route.fulfill({ json: { items: [node] } })
      return
    }
    if (path === '/api/v1/mcp/servers' && request.method() === 'GET') {
      await route.fulfill({ json: { items: [server] } })
      return
    }
    if (path === `/api/v1/mcp/servers/${serverID}` && request.method() === 'PATCH') {
      const body = request.postDataJSON() as Record<string, unknown>
      mcpPatchBodies.push(body)
      if (!body.confirmTargets) {
        await route.fulfill({ status: 409, json: { error: { code: 'secret_confirmation_required', message: 'Confirm affected targets', envKeys: ['SEARCH_TOKEN'], headerKeys: [], targets: [{ nodeId: nodeID, nodeName: node.name, runtime: 'codex' }] } } })
      } else {
        await route.fulfill({ json: server })
      }
      return
    }
    if (path === '/api/v1/mcp/profiles' || path === '/api/v1/mcp/deployments' || path === '/api/v1/shared-sources') {
      await route.fulfill({ json: { items: [] } })
      return
    }
    await route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/skills')
  await expect(page.getByRole('heading', { name: 'Skills' })).toBeVisible()
  await page.getByRole('button', { name: 'Discovered', exact: true }).click()
  const hermesSkillRow = page.getByRole('row').filter({ hasText: 'Hermes Formatter' })
  const codexSkillRow = page.getByRole('row').filter({ hasText: 'Codex Local Skill' })
  await expect(hermesSkillRow.getByRole('button', { name: 'Import snapshot' })).toBeVisible()
  await expect(hermesSkillRow.getByRole('button', { name: 'Adopt' })).toHaveCount(0)
  await expect(codexSkillRow.getByRole('button', { name: 'Adopt' })).toBeVisible()
  await hermesSkillRow.getByRole('button', { name: 'Import snapshot' }).click()
  await expect.poll(() => skillImportBody).toEqual({ expectedSha256: skillSHA })

  await page.getByRole('button', { name: 'Library', exact: true }).click()
  await page.getByRole('row').filter({ hasText: librarySkill.name }).getByRole('button', { name: 'Set deployment targets' }).click()
  const targetDialog = page.getByRole('dialog', { name: `Targets · ${librarySkill.name}` })
  await expect(targetDialog.locator('.matrix-head span')).toHaveText(['Node', 'codex', 'claude', 'grok', 'openclaw'])
  await expect(targetDialog.getByText('hermes', { exact: true })).toHaveCount(0)
  await targetDialog.getByRole('button', { name: 'Close' }).click()

  await page.goto('/mcp')
  await expect(page.getByRole('heading', { name: 'MCP', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Discovered', exact: true }).click()
  const mcpRow = page.getByRole('row').filter({ hasText: 'hermes-search' })
  await expect(mcpRow).toContainText('SEARCH_TOKEN')
  await expect(mcpRow).toContainText('Authorization')
  await mcpRow.getByRole('button', { name: 'Import' }).click()
  const importDialog = page.getByRole('dialog', { name: 'Confirm Hermes MCP import' })
  await expect(importDialog).toContainText('env · SEARCH_TOKEN')
  await expect(importDialog).toContainText('header · Authorization')
  await importDialog.getByRole('button', { name: 'Capture and import' }).click()
  await expect(page.getByText('Hermes MCP import queued.')).toBeVisible()
  expect(mcpImportBodies).toEqual([
    { observedGeneration: 7, confirmSecrets: false },
    { observedGeneration: 7, confirmSecrets: true },
  ])

  await page.getByRole('button', { name: 'Servers', exact: true }).click()
  await page.getByRole('row').filter({ hasText: server.name }).getByRole('button', { name: 'Edit server' }).click()
  const editor = page.getByRole('dialog', { name: `Edit MCP server · ${server.name}` })
  const environmentEditor = editor.locator('.secret-editor').filter({ hasText: 'Environment secrets' })
  await environmentEditor.getByLabel('Set / replace').fill('SEARCH_TOKEN=rotated-browser-fixture')
  await editor.getByRole('button', { name: 'Save changes' }).click()
  await expect(editor.getByText('Secret-key confirmation required')).toBeVisible()
  await expect(editor).toContainText(`${node.name} · codex`)
  await editor.getByRole('button', { name: 'Confirm affected targets' }).click()
  await expect(page.getByText('MCP server updated.')).toBeVisible()
  expect(mcpPatchBodies).toHaveLength(2)
  expect(mcpPatchBodies[0]).toMatchObject({ confirmTargets: false, secretChanges: { env: { set: { SEARCH_TOKEN: 'rotated-browser-fixture' }, remove: [] } } })
  expect(mcpPatchBodies[1]).toMatchObject({ confirmTargets: true, secretChanges: { env: { set: { SEARCH_TOKEN: 'rotated-browser-fixture' }, remove: [] } } })
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-hermes-imports.png`, fullPage: true })
  expect(expectedConflictErrors).toHaveLength(2)
  expect(consoleErrors).toEqual([])
})

test('operator confirms named secrets before remote Profile activation', async ({ page }) => {
  const profileID = '22222222-2222-4222-8222-222222222222'
  const nodeID = '11111111-1111-4111-8111-111111111111'
  let activationBody: { confirmSecrets?: boolean } | null = null
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') {
      await route.fulfill({ json: { authenticated: true, csrfToken: 'operator-csrf', user: { id: '33333333-3333-4333-8333-333333333333', username: 'operator', email: 'operator@toolhub.local', displayName: 'Operator', roles: ['operator'] } } })
      return
    }
    if (path === '/api/v1/profiles' && request.method() === 'GET') {
      await route.fulfill({ json: { items: [{ id: profileID, name: 'Research', description: 'Remote fixture', mcpServerCount: 1, skillCount: 0, activationCount: 0 }] } })
      return
    }
    if (path === '/api/v1/skills' || path === '/api/v1/mcp/servers') {
      await route.fulfill({ json: { items: [] } })
      return
    }
    if (path === '/api/v1/nodes') {
      await route.fulfill({ json: { items: [{ id: nodeID, name: 'remote-build-node', status: 'online', isLocal: false, runtimeKinds: ['codex', 'hermes'], activations: [] }] } })
      return
    }
    if (path === `/api/v1/profiles/${profileID}/preflight`) {
      await route.fulfill({ status: 409, json: { error: { code: 'remote_secret_confirmation_required', message: 'Confirm named secrets', issues: [{ code: 'remote_secret_confirmation_required', scope: 'mcp', detail: 'Confirm the named secret keys before remote delivery' }], skipped: [], nodeName: 'remote-build-node', secretKeys: ['TAVILY_API_KEY'] } } })
      return
    }
    if (path === `/api/v1/profiles/${profileID}/activate`) {
      activationBody = request.postDataJSON()
      await route.fulfill({ status: 202, json: { id: '44444444-4444-4444-8444-444444444444', kind: 'profile_activate', state: 'pending' } })
      return
    }
    await route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/profiles')
  await page.getByRole('button', { name: 'Activate Profile' }).click()
  const activationDialog = page.getByRole('dialog', { name: 'Activate Profile · Research' })
  await expect(activationDialog).toBeVisible()
  await expect(activationDialog.getByLabel('Runtime').locator('option')).toHaveText(['codex'])
  await page.getByRole('button', { name: 'Run preflight' }).click()
  await expect(page.getByText('TAVILY_API_KEY')).toBeVisible()
  await page.getByRole('button', { name: 'Review secret keys' }).click()
  await expect(page.getByRole('dialog', { name: 'Confirm remote secret delivery' })).toContainText('remote-build-node')
  await page.getByRole('button', { name: 'Confirm and activate' }).click()
  await expect(page.getByText('Profile activation queued.')).toBeVisible()
  expect(activationBody?.confirmSecrets).toBe(true)
})
