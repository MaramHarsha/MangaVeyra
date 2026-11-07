export class APIError extends Error {constructor(readonly status:number,message:string){super(message)}}
export async function request<T>(url:string,method='GET',body?:unknown,signal?:AbortSignal):Promise<T>{
 const response=await fetch(url,{method,credentials:'same-origin',headers:body instanceof FormData?undefined:body===undefined?undefined:{'Content-Type':'application/json'},body:body===undefined?undefined:body instanceof FormData?body:JSON.stringify(body),signal,cache:'no-store'});
 const data=await response.json().catch(()=>null);if(!response.ok)throw new APIError(response.status,data?.error?.message||String(response.status));return data as T;
}
