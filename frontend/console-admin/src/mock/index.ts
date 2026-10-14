import { authHandlers } from './handlers/auth'
import { menuHandlers } from './handlers/menu'
import { userHandlers } from './handlers/user'
import { roleHandlers } from './handlers/role'
import { dashboardHandlers } from './handlers/dashboard'
import { securityHandlers } from './handlers/security'

export const handlers = [
  ...authHandlers,
  ...menuHandlers,
  ...userHandlers,
  ...roleHandlers,
  ...dashboardHandlers,
  ...securityHandlers,
]
