// 商品实体（bff /api/products 返回结构）
export interface Product {
  id: number
  name: string
  price: number
  category: string
  stock: number
  description: string
  created_at?: string
}
