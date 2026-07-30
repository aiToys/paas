import { describe, it, expect, beforeEach } from 'vitest'
import axios, { type AxiosInstance, type InternalAxiosRequestConfig } from 'axios'
import { installInterceptors } from './interceptors'
import { HttpError } from '@/lib/error/types'

// 构造一个挂载真实拦截器的 axios 实例 + mock adapter 返回指定 data。
function newClient(respData: unknown, status = 200): AxiosInstance {
  const inst = axios.create({ baseURL: 'http://test' })
  installInterceptors(inst)
  inst.defaults.adapter = (config: InternalAxiosRequestConfig) =>
    Promise.resolve({
      data: respData,
      status,
      statusText: 'OK',
      headers: {},
      config
    } as any)
  return inst
}

describe('响应拦截器：PaaS core 格式兼容', () => {
  beforeEach(() => {
    sessionStorage.clear()
  })

  it('core {data:T}（无 code）应解包为 T', async () => {
    const client = newClient({ data: { a: 1, b: 'x' } })
    const res = await client.get('/x')
    expect(res.data).toEqual({ a: 1, b: 'x' })
  })

  it('ApiResult {code:0,data} 也正确解包', async () => {
    const client = newClient({ code: 0, data: [1, 2, 3], msg: '' })
    const res = await client.get('/x')
    expect(res.data).toEqual([1, 2, 3])
  })

  it('core {error:msg} + HTTP 401 抛 HttpError 且映射 msg', async () => {
    const inst = axios.create({ baseURL: 'http://test' })
    installInterceptors(inst)
    inst.defaults.adapter = (config: InternalAxiosRequestConfig) =>
      Promise.reject({
        response: { data: { error: '用户名或密码错误' }, status: 401, statusText: 'Unauthorized', headers: {}, config },
        config,
        isAxiosError: true
      } as any)
    try {
      await inst.get('/x')
      expect.unreachable('应抛错')
    } catch (e) {
      expect(e).toBeInstanceOf(HttpError)
      const p = (e as HttpError).problem
      expect(p.title).toBe('用户名或密码错误')
      expect(p.detail).toBe('用户名或密码错误')
    }
  })
})
