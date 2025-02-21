let htmlEl = document.getElementsByTagName("html")[0];
let cms_fields_str = htmlEl.getAttribute("x-data");
let cms_fields = eval("(" + cms_fields_str + ")");

function createInputs(obj, container) {
    for (const key in obj) {
        if (Object.hasOwnProperty.call(obj, key)) {
            const input = document.createElement('input');
            input.type = 'text';
            let attribute = "x-model";
            if (typeof obj[key] === 'number') {
                input.type = 'number';
                attribute += ".number";
            }
            input.id = key;
            input.name = key;
            input.placeholder = key;
            input.setAttribute(attribute, key);
            const label = document.createElement('label');
            label.htmlFor = key;
            label.textContent = key;
            const div = document.createElement('div').appendChild(label).parentNode;
            container.appendChild(document.createElement('br'));
            container.appendChild(document.createElement('br'));
            container.appendChild(div);
            container.appendChild(input);
        }
    }
}
const cms = document.getElementById('plenti_cms');
createInputs(cms_fields, cms);

document.getElementById('toggle_plenti_cms').addEventListener('click', function () {
    const menu = document.getElementById('plenti_cms');
    menu.classList.toggle('menu-visible');
});
