'use strict';
const loginForm=document.querySelector('form');
loginForm.addEventListener('submit',async event=>{
  event.preventDefault();
  const button=loginForm.querySelector('button'),error=document.getElementById('loginError');
  button.disabled=true;error.textContent='';
  try{
    const response=await fetch('/login',{method:'POST',body:new URLSearchParams(new FormData(loginForm)),credentials:'same-origin'});
    if(response.ok&&response.redirected){loginForm.reset();location.replace('/');return;}
    error.textContent=response.status===429?'尝试过多，请一分钟后再试。':'登录失败，请检查密码或刷新页面后再试。';
  }catch{error.textContent='连接失败，请稍后重试。';}
  finally{button.disabled=false;}
});
